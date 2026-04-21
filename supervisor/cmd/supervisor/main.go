package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/events"
	"github.com/garrison-hq/garrison/supervisor/internal/health"
	"github.com/garrison-hq/garrison/supervisor/internal/pgdb"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"
)

// Exit codes — contracts/cli.md §"Exit codes — full table". Exported as
// constants so tests can assert on them without hard-coded integers.
const (
	ExitOK                = 0
	ExitFailure           = 1
	ExitUsage             = 2
	ExitMigrateFailed     = 3
	ExitAdvisoryLockHeld  = 4
	ExitSigkillEscalation = 5
)

const usage = `Usage: supervisor [FLAGS]

Garrison supervisor daemon. Listens on Postgres pg_notify and spawns
agent subprocesses on work.ticket.created events.

Flags:
  --version         Print version and exit.
  --help, -h        Print this message and exit.
  --migrate         Run goose migrations against ORG_OS_DATABASE_URL and exit.

Environment variables (daemon mode):
  ORG_OS_DATABASE_URL        required   Postgres connection URL.
  ORG_OS_FAKE_AGENT_CMD      required   Command template for the fake agent.
  ORG_OS_POLL_INTERVAL       5s         Fallback poll interval (min 1s).
  ORG_OS_SUBPROCESS_TIMEOUT  60s        Per-subprocess timeout.
  ORG_OS_SHUTDOWN_GRACE      30s        Graceful shutdown deadline.
  ORG_OS_HEALTH_PORT         8080       HTTP health server port.
  ORG_OS_LOG_LEVEL           info       Log level (debug|info|warn|error).
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stdout, usage) }

	showVersion := fs.Bool("version", false, "print version and exit")
	showHelp := fs.Bool("help", false, "print help and exit")
	fs.BoolVar(showHelp, "h", false, "shorthand for --help")
	doMigrate := fs.Bool("migrate", false, "run goose migrations and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return ExitOK
		}
		return ExitUsage
	}

	if *showHelp {
		fs.Usage()
		return ExitOK
	}
	if *showVersion {
		fmt.Fprintf(os.Stdout, "supervisor %s\n", version)
		return ExitOK
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "supervisor: unexpected positional arguments: %v\n", fs.Args())
		fs.Usage()
		return ExitUsage
	}

	if *doMigrate {
		return runMigrate()
	}

	return runDaemon()
}

// runDaemon loads config, connects to Postgres with FR-017 backoff, acquires
// the FR-018 advisory lock, then runs the subsystems under an errgroup until
// SIGTERM/SIGINT/SIGHUP cancels the root context.
func runDaemon() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervisor: %v\n", err)
		return ExitFailure
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	logger.Info("supervisor starting", "version", version)
	logger.Info("config loaded",
		"poll_interval", cfg.PollInterval,
		"subprocess_timeout", cfg.SubprocessTimeout,
		"shutdown_grace", cfg.ShutdownGrace,
		"health_port", cfg.HealthPort)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Install signal handlers before any potentially long-running operation
	// (pgdb.Connect can loop in FR-017 backoff; advisory-lock acquire can
	// block). Without this, a SIGTERM delivered during that window would hit
	// the Go default handler and exit with code 143, violating the
	// contracts/cli.md graceful-shutdown contract.
	sigCh, stopSignals := installSignalHandler()
	defer stopSignals()
	go watchSignals(ctx, sigCh, cancel, logger)

	// FR-017 initial connect with 100ms→30s backoff. Connect blocks until
	// success or ctx cancel, so a signal during this window triggers a clean
	// early exit with no lock acquisition.
	pool, listenConn, err := pgdb.Connect(ctx, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info("shutdown during initial connect")
			return ExitOK
		}
		logger.Error("initial connect failed", "error", err)
		return ExitFailure
	}
	defer pool.Close()
	defer func() { _ = listenConn.Close(context.Background()) }()
	logger.Info("connected to Postgres")

	// FR-018 advisory lock on the dedicated listen conn. Lock is session-bound,
	// released implicitly when the conn closes above.
	if err := pgdb.AcquireAdvisoryLock(ctx, listenConn); err != nil {
		if errors.Is(err, pgdb.ErrAdvisoryLockHeld) {
			logger.Error("advisory lock held by another supervisor; exiting")
			return ExitAdvisoryLockHeld
		}
		logger.Error("advisory lock acquisition failed", "error", err)
		return ExitFailure
	}
	logger.Info("advisory lock acquired")

	queries := store.New(pool)
	state := health.NewState()
	var sigkillCounter atomic.Int64

	// Build the spawn dependency bundle once; the handler closure captures it.
	spawnDeps := spawn.Deps{
		Pool:               pool,
		Queries:            queries,
		FakeAgentCmd:       cfg.FakeAgentCmd,
		SubprocessTimeout:  cfg.SubprocessTimeout,
		Logger:             logger,
		SigkillEscalations: &sigkillCounter,
	}

	// Static dispatcher (FR-014). `work.ticket.created` is the only M1 channel.
	ticketCreatedHandler := func(ctx context.Context, eventID pgtype.UUID) error {
		return spawn.Spawn(ctx, spawnDeps, eventID)
	}
	dispatcher := events.NewDispatcher(map[string]events.Handler{
		"work.ticket.created": ticketCreatedHandler,
	})

	healthServer := health.NewServer(cfg, state, pool)

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return events.Run(gctx, events.Deps{
			Pool:              pool,
			Queries:           queries,
			InitialListenConn: listenConn,
			Dialer:            pgdb.NewRealDialer(),
			DatabaseURL:       cfg.DatabaseURL,
			Dispatcher:        dispatcher,
			State:             state,
			PollInterval:      cfg.PollInterval,
			Logger:            logger,
		})
	})

	g.Go(func() error {
		logger.Info("health server listening", "addr", fmt.Sprintf("0.0.0.0:%d", cfg.HealthPort))
		return healthServer.Serve(gctx)
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("subsystem exited with error", "error", err)
		return ExitFailure
	}

	if n := sigkillCounter.Load(); n > 0 {
		logger.Warn("shutdown: one or more subprocesses required SIGKILL", "count", n)
		return ExitSigkillEscalation
	}
	logger.Info("shutdown complete")
	return ExitOK
}
