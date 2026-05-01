package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/chat"
	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/dashboardapi"
	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
	"github.com/garrison-hq/garrison/supervisor/internal/events"
	"github.com/garrison-hq/garrison/supervisor/internal/health"
	"github.com/garrison-hq/garrison/supervisor/internal/hygiene"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
	"github.com/garrison-hq/garrison/supervisor/internal/pgdb"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
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

// EngineeringTicketChannel is the qualified pg_notify channel the M2.1
// dispatcher registers against. After M2.1 ships, introducing a second
// department is purely additive (register the additional channel) per
// plan.md §internal/events.
// EngineeringTicketChannel was M2.1's sole engineering handler channel
// (work.ticket.created.engineering.todo). M2.2 shifts to the in_dev
// column for the engineer spawn trigger per Session 2026-04-23 and
// registers a second channel for the qa-engineer. Kept here as a const
// for any test harness that still references it.
const EngineeringTicketChannel = "work.ticket.created.engineering.todo"

// M2.2 channel constants — the supervisor registers these two handlers
// per Session 2026-04-23 and FR-227/FR-228.
const (
	EngineeringInDevChannel    = "work.ticket.created.engineering.in_dev"
	EngineeringQAReviewChannel = "work.ticket.transitioned.engineering.in_dev.qa_review"
)

const usage = `Usage: supervisor [FLAGS] | mcp-postgres | mcp finalize | mcp garrison-mutate

Garrison supervisor daemon. Listens on Postgres pg_notify and spawns
Claude Code subprocesses on work.ticket.created.<dept>.<column> events.

Subcommands:
  mcp-postgres          Run the in-tree Postgres MCP server on stdio.
                        Used by Claude Code via --mcp-config. Reads
                        GARRISON_PGMCP_DSN from env.
  mcp finalize          Run the in-tree finalize_ticket MCP server on
                        stdio (M2.2.1). Used by Claude Code via
                        --mcp-config. Reads GARRISON_AGENT_INSTANCE_ID
                        and GARRISON_DATABASE_URL from env.

Flags:
  --version             Print version and exit.
  --help, -h            Print this message and exit.
  --migrate             Run goose migrations against GARRISON_DATABASE_URL and exit.

Environment variables (daemon mode):
  GARRISON_DATABASE_URL        required   Postgres connection URL.
  GARRISON_AGENT_RO_PASSWORD   required   Password for the garrison_agent_ro role.
  GARRISON_CLAUDE_BIN          optional   Absolute path to the claude binary.
                                          Falls back to exec.LookPath("claude").
  GARRISON_CLAUDE_MODEL        optional   Model override; empty = per-agent DB value.
  GARRISON_CLAUDE_BUDGET_USD   0.05       Per-invocation --max-budget-usd.
  GARRISON_MCP_CONFIG_DIR      /var/lib/garrison/mcp/   Directory for per-spawn MCP configs.
  GARRISON_POLL_INTERVAL       5s         Fallback poll interval (min 1s).
  GARRISON_SUBPROCESS_TIMEOUT  60s        Per-subprocess timeout.
  GARRISON_SHUTDOWN_GRACE      30s        Graceful shutdown deadline.
  GARRISON_HEALTH_PORT         8080       HTTP health server port.
  GARRISON_LOG_LEVEL           info       Log level (debug|info|warn|error).
  GARRISON_FAKE_AGENT_CMD      (test-only) Flip into fake-agent mode; suppresses
                                          the real-Claude precondition checks.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// Subcommand dispatch takes the first positional arg. mcp-postgres
	// runs a different code path that does not load the daemon config
	// (it only needs GARRISON_PGMCP_DSN). Handling it before flag.Parse
	// keeps a missing GARRISON_DATABASE_URL from shadowing the missing
	// GARRISON_PGMCP_DSN error.
	if len(args) > 0 && args[0] == "mcp-postgres" {
		return runMCPPostgres()
	}
	// M2.2.1 T005: `supervisor mcp finalize` — the in-tree finalize_ticket
	// MCP server. Invoked per-spawn by Claude via the per-invocation MCP
	// config (see internal/mcpconfig). Shape mirrors mcp-postgres: early
	// dispatch before flag parsing so env-only deps don't trip daemon
	// config validation.
	if len(args) >= 2 && args[0] == "mcp" && args[1] == "finalize" {
		return runMCPFinalize()
	}
	// M5.3 T004: `supervisor mcp garrison-mutate` — the in-tree
	// chat-driven mutation MCP server. Invoked per chat-message spawn
	// by Claude via the per-message MCP config (see
	// internal/mcpconfig.BuildChatConfig). Same dispatch shape as
	// finalize: env-only deps, JSON-RPC over stdio, exits on stdin EOF
	// or SIGTERM.
	if len(args) >= 2 && args[0] == "mcp" && args[1] == "garrison-mutate" {
		return runMCPGarrisonMutate()
	}

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
	logDaemonStartupConfig(logger, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh, stopSignals := installSignalHandler()
	defer stopSignals()
	go watchSignals(ctx, sigCh, cancel, logger)

	pool, listenConn, exitCode := connectAndLock(ctx, cfg, logger)
	if pool == nil {
		return exitCode
	}
	defer pool.Close()
	defer func() { _ = listenConn.Close(context.Background()) }()

	queries := store.New(pool)
	state := health.NewState()
	var sigkillCounter atomic.Int64

	if exitCode := runPalaceBootstrap(ctx, cfg, logger); exitCode != ExitOK {
		return exitCode
	}

	// Agents cache is populated at startup; hot-reload is deferred per
	// plan §internal/agents. In fake-agent mode the cache is still built
	// (it's cheap and harmless) so tests can exercise it incidentally.
	agentsCache, err := agents.NewCache(ctx, queries)
	if err != nil {
		logger.Error("agents cache init failed", "error", err)
		return ExitFailure
	}
	logger.Info("agents cache loaded", "count", agentsCache.Len())

	supervisorBin := resolveSupervisorBin(logger)
	sharedPalaceClient := buildSharedPalaceClient(cfg)

	vaultClient, exitCode := buildVaultClient(ctx, cfg, logger)
	if exitCode != ExitOK {
		return exitCode
	}

	spawnDeps := spawn.Deps{
		Pool:               pool,
		Queries:            queries,
		Logger:             logger,
		SubprocessTimeout:  cfg.SubprocessTimeout,
		SigkillEscalations: &sigkillCounter,
		FakeAgentCmd:       cfg.FakeAgentCmd,
		UseFakeAgent:       cfg.UseFakeAgent,
		AgentsCache:        agentsCache,
		ClaudeBin:          cfg.ClaudeBin,
		ClaudeModel:        cfg.ClaudeModel,
		ClaudeBudgetUSD:    cfg.ClaudeBudgetUSD,
		MCPConfigDir:       cfg.MCPConfigDir,
		SupervisorBin:      supervisorBin,
		AgentRODSN:         cfg.AgentRODSN(),
		// M2.2 — docker/mempalace fields for MCP-config building + wake-up.
		DockerBin:          cfg.DockerBin,
		MempalaceContainer: cfg.MempalaceContainer,
		PalacePath:         cfg.PalacePath,
		DockerHost:         cfg.DockerHost,
		// M2.2.1 — palace + timeout for the atomic finalize writer.
		Palace:               sharedPalaceClient,
		FinalizeWriteTimeout: cfg.FinalizeWriteTimeout,
		// M2.3 — vault client + customer scope for secret injection.
		Vault:      vaultClient,
		CustomerID: cfg.CustomerID(),
	}

	engineerHandler := func(ctx context.Context, eventID pgtype.UUID) error {
		return spawn.Spawn(ctx, spawnDeps, eventID, "engineer")
	}
	qaEngineerHandler := func(ctx context.Context, eventID pgtype.UUID) error {
		return spawn.Spawn(ctx, spawnDeps, eventID, "qa-engineer")
	}
	dispatcher := events.NewDispatcher(map[string]events.Handler{
		// M2.2 canonical channels: engineer listens_for was shifted from
		// `todo` to `in_dev` per Session 2026-04-23. The engineer agent
		// row's listens_for data carries in_dev only. The dispatcher also
		// routes the legacy `created.engineering.todo` channel to the same
		// engineer handler so M1/M2.1 chaos tests + any operator workflow
		// that inserts tickets at the default `todo` column continues to
		// work. The clarification forbids registering a *separate agent*
		// against todo; it doesn't constrain which channels the *dispatcher*
		// maps to the existing engineer.
		EngineeringTicketChannel:   engineerHandler, // "created.engineering.todo" (M1/M2.1 back-compat)
		EngineeringInDevChannel:    engineerHandler, // M2.2 canonical
		EngineeringQAReviewChannel: qaEngineerHandler,
	})

	healthServer := health.NewServer(cfg, state, pool)

	// M5.4 — objstore client + dashboardapi server. Both are gated on
	// vault availability: in fake-agent mode or when GARRISON_INFISICAL_ADDR
	// is unset (M2.1/M2.2 chaos-test path), the supervisor skips this
	// surface entirely. In production (vault configured), MinIO bootstrap
	// is fail-closed: an unreachable MinIO at boot causes ExitFailure.
	objstoreClient, exitCode := buildObjstoreClient(ctx, cfg, vaultClient, logger)
	if exitCode != ExitOK {
		return exitCode
	}
	dashboardAPIServer := buildDashboardAPIServer(cfg, pool, objstoreClient, sharedPalaceClient, logger)

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

	startDashboardAPIServerIfWired(g, gctx, dashboardAPIServer, cfg, logger)

	// M4 — agents.changed cache invalidator (T014 / FR-100).
	// The dashboard's editAgent server action emits
	// pg_notify('agents.changed', role_slug) on every successful
	// agents-row write; the listener drives Cache.Reset so the
	// next spawn picks up the new config. The listener owns its
	// own dedicated pgx.Conn (LISTEN is connection-scoped) and
	// cleanly exits on root-context cancellation per AGENTS.md
	// §Concurrency rule 1.
	if err := agents.StartChangeListener(gctx, pool, agentsCache); err != nil {
		logger.Warn("agents.changed listener failed to start; agent edits will not propagate to the supervisor cache until restart", "err", err)
	}

	// M2.2 — hygiene listener + sweep. Both goroutines join the errgroup
	// so a SIGTERM cascades to them via root-ctx cancellation; each one
	// finishes its in-flight UPDATE through context.WithoutCancel +
	// TerminalWriteGrace per FR-217. GARRISON_DISABLE_PALACE_BOOTSTRAP
	// gates these off alongside the bootstrap itself (same test-hook).
	if !cfg.UseFakeAgent && !cfg.DisablePalaceBootstrap {
		hygieneDeps := hygiene.Deps{
			DSN:                cfg.AgentMempalaceDSN(),
			Dialer:             pgdb.NewRealDialer(),
			Queries:            queries,
			Palace:             sharedPalaceClient, // M2.2.1 T011: reuse the spawn-deps palace client
			Logger:             logger,
			Delay:              cfg.HygieneDelay,
			SweepInterval:      cfg.HygieneSweepInterval,
			TerminalWriteGrace: spawn.TerminalWriteGrace,
			Channels:           []string{EngineeringQAReviewChannel, "work.ticket.transitioned.engineering.qa_review.done"},
		}
		g.Go(func() error {
			return hygiene.RunListener(gctx, hygieneDeps)
		})
		g.Go(func() error {
			return hygiene.RunSweep(gctx, hygieneDeps)
		})
	}

	// M5.1 — chat backend subsystem. RestartSweep runs once
	// synchronously before the listener starts LISTEN; the listener +
	// idle sweep join the errgroup so SIGTERM cascades cleanly via
	// root-ctx cancellation per AGENTS.md concurrency rule 1.
	chatDeps := chat.Deps{
		Pool:                 pool,
		Queries:              queries,
		VaultClient:          vaultClient,
		DockerExec:           dockerexec.RealDockerExec{DockerBin: cfg.DockerBin},
		Logger:               logger,
		CustomerID:           cfg.CustomerID(),
		OAuthVaultPath:       cfg.ChatOAuthVaultPath,
		ChatContainerImage:   cfg.ChatContainerImage,
		MCPConfigDir:         cfg.MCPConfigDir,
		DockerNetwork:        cfg.ChatDockerNetwork,
		TurnTimeout:          cfg.ChatTurnTimeout,
		SessionIdleTimeout:   cfg.ChatSessionIdleTimeout,
		SessionCostCapUSD:    cfg.ChatSessionCostCapUSD,
		TerminalWriteGrace:   spawn.TerminalWriteGrace,
		ShutdownSignalGrace:  spawn.ShutdownSignalGrace,
		ClaudeBinInContainer: "/usr/local/bin/claude",
		DefaultModel:         cfg.ChatDefaultModel,
	}
	if err := chat.RunRestartSweep(ctx, chatDeps); err != nil {
		logger.Warn("chat: restart sweep failed; continuing", "err", err)
	}
	// GARRISON_CHAT_INTERNAL_DOCKER_HOST + GARRISON_CHAT_INTERNAL_AGENT_RO_DSN
	// untangle the supervisor-side and chat-container-side values when the
	// two surfaces aren't on the same network. The supervisor on host uses
	// localhost-flavoured DSN/DOCKER_HOST; the chat container spawns into
	// garrison-net and must reach `garrison-dev-pg:5432` /
	// `garrison-docker-proxy:2375` via Docker DNS. Both env vars fall back
	// to the matching cfg.* value when unset (matches the production
	// compose where supervisor + chat share the network).
	chatInternalDockerHost := cfg.DockerHost
	if v := os.Getenv("GARRISON_CHAT_INTERNAL_DOCKER_HOST"); v != "" {
		chatInternalDockerHost = v
	}
	chatInternalAgentRODSN := cfg.AgentRODSN()
	if v := os.Getenv("GARRISON_CHAT_INTERNAL_AGENT_RO_DSN"); v != "" {
		chatInternalAgentRODSN = v
	}
	chatWorker := chat.NewWorker(chatDeps, supervisorBin, chatInternalAgentRODSN, chat.MempalaceWiring{
		DockerBin:          cfg.DockerBin,
		MempalaceContainer: cfg.MempalaceContainer,
		PalacePath:         cfg.PalacePath,
		DockerHost:         chatInternalDockerHost,
	})
	g.Go(func() error {
		return chat.RunListener(gctx, chatDeps, chatWorker)
	})
	g.Go(func() error {
		return chat.RunIdleSweep(gctx, chatDeps)
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

// logDaemonStartupConfig emits the "supervisor starting" + "config
// loaded" pair at startup. Pure logging; pulled out to reduce
// runDaemon's line count without changing semantics.
func logDaemonStartupConfig(logger *slog.Logger, cfg *config.Config) {
	logger.Info("supervisor starting", "version", version)
	logger.Info("config loaded",
		"poll_interval", cfg.PollInterval,
		"subprocess_timeout", cfg.SubprocessTimeout,
		"shutdown_grace", cfg.ShutdownGrace,
		"health_port", cfg.HealthPort,
		"use_fake_agent", cfg.UseFakeAgent,
		"claude_bin", cfg.ClaudeBin,
		"mcp_config_dir", cfg.MCPConfigDir,
	)
}

// connectAndLock dials Postgres with FR-017 backoff and acquires the
// FR-018 advisory lock on the listen connection. Returns (nil, nil,
// exitCode) on any failure path so runDaemon can short-circuit.
func connectAndLock(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*pgxpool.Pool, *pgx.Conn, int) {
	pool, listenConn, err := pgdb.Connect(ctx, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			logger.Info("shutdown during initial connect")
			return nil, nil, ExitOK
		}
		logger.Error("initial connect failed", "error", err)
		return nil, nil, ExitFailure
	}
	logger.Info("connected to Postgres")
	if err := pgdb.AcquireAdvisoryLock(ctx, listenConn); err != nil {
		_ = listenConn.Close(context.Background())
		pool.Close()
		if errors.Is(err, pgdb.ErrAdvisoryLockHeld) {
			logger.Error("advisory lock held by another supervisor; exiting")
			return nil, nil, ExitAdvisoryLockHeld
		}
		logger.Error("advisory lock acquisition failed", "error", err)
		return nil, nil, ExitFailure
	}
	logger.Info("advisory lock acquired")
	return pool, listenConn, ExitOK
}

// runPalaceBootstrap runs `mempalace init --yes` against the sidecar
// container before the agents cache loads, so a broken palace surface
// halts startup cleanly. T001 finding F1: the operation is idempotent
// in MemPalace 3.3.2. Fake-agent mode and the test-hook
// GARRISON_DISABLE_PALACE_BOOTSTRAP both skip this.
func runPalaceBootstrap(ctx context.Context, cfg *config.Config, logger *slog.Logger) int {
	if cfg.UseFakeAgent || cfg.DisablePalaceBootstrap {
		return ExitOK
	}
	if err := mempalace.Bootstrap(ctx, mempalace.BootstrapConfig{
		DockerBin:          cfg.DockerBin,
		MempalaceContainer: cfg.MempalaceContainer,
		PalacePath:         cfg.PalacePath,
		Logger:             logger,
		InitTimeout:        30 * time.Second,
	}); err != nil {
		logger.Error("palace bootstrap failed", "error", err,
			"container", cfg.MempalaceContainer, "path", cfg.PalacePath)
		return ExitFailure
	}
	return ExitOK
}

// resolveSupervisorBin returns the absolute path the supervisor writes
// into mcp-config-<uuid>.json so Claude's MCP launcher can exec
// `supervisor mcp-postgres`. os.Executable is the portable way to get
// that path; on failure we fall back to os.Args[0] and rely on
// spawn_failed to surface a non-executable resolved path.
//
// GARRISON_SUPERVISOR_BIN_OVERRIDE is a test-only hook (T018 chaos test
// uses it to point the MCP config at /bin/does-not-exist so Claude
// reports postgres.status=failed at init). Production never sets it.
func resolveSupervisorBin(logger *slog.Logger) string {
	if override := os.Getenv("GARRISON_SUPERVISOR_BIN_OVERRIDE"); override != "" {
		logger.Warn("GARRISON_SUPERVISOR_BIN_OVERRIDE active; MCP config will point at the override path",
			"override", override)
		return override
	}
	exe, err := os.Executable()
	if err != nil {
		logger.Warn("os.Executable failed; using os.Args[0]", "err", err)
		return os.Args[0]
	}
	return exe
}

// buildSharedPalaceClient constructs the M2.2.1 T011 palace client
// shared across the hygiene listener and the finalize atomic writer.
// Returns nil for the fake-agent and bootstrap-disabled paths so callers
// can hold a pointer unconditionally and rely on WriteFinalize's
// nil-guard to skip the atomic write.
func buildSharedPalaceClient(cfg *config.Config) *mempalace.Client {
	if cfg.UseFakeAgent || cfg.DisablePalaceBootstrap {
		return nil
	}
	return &mempalace.Client{
		DockerBin:          cfg.DockerBin,
		MempalaceContainer: cfg.MempalaceContainer,
		PalacePath:         cfg.PalacePath,
		DockerHost:         cfg.DockerHost,
		Timeout:            10 * time.Second,
		Exec:               dockerexec.RealDockerExec{DockerBin: cfg.DockerBin},
	}
}

// startDashboardAPIServerIfWired registers the dashboardapi server's
// Serve goroutine on the errgroup when buildDashboardAPIServer
// produced a non-nil server (i.e., not in fake-agent mode and vault
// is configured). Pulled out of runDaemon to keep its cognitive
// complexity within Sonar's threshold.
func startDashboardAPIServerIfWired(
	g *errgroup.Group,
	gctx context.Context,
	srv *dashboardapi.Server,
	cfg *config.Config,
	logger *slog.Logger,
) {
	if srv == nil {
		return
	}
	g.Go(func() error {
		logger.Info("dashboardapi server listening", "addr", fmt.Sprintf("0.0.0.0:%d", cfg.DashboardAPIPort))
		return srv.Serve(gctx)
	})
}

// uuidString renders a pgtype.UUID as the canonical 8-4-4-4-12
// hyphen-separated form used across the codebase (see existing
// buildVaultClient site).
func uuidString(u pgtype.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}

// buildObjstoreClient fetches the scoped MinIO credentials from
// Infisical and constructs an objstore.Client + runs BootstrapBucket.
// M2.2 mempalace-bootstrap parallel: fail-closed at boot — an
// unreachable MinIO causes ExitFailure.
//
// Returns (nil, ExitOK) when M5.4 is gracefully skipped: fake-agent
// mode or vault disabled (no Infisical address). This preserves
// M2.1/M2.2 chaos-test compatibility — those tests run without
// Infisical.
func buildObjstoreClient(ctx context.Context, cfg *config.Config, vaultClient vault.Fetcher, logger *slog.Logger) (*objstore.Client, int) {
	if cfg.UseFakeAgent || vaultClient == nil {
		logger.Info("objstore: skipping M5.4 wiring (fake-agent or vault unavailable)")
		return nil, ExitOK
	}

	// Fetch the two scoped MinIO secrets in one Fetch call. The vault
	// client writes vault_access_log rows per M2.3 Rule 4; failures
	// short-circuit (fail-closed at boot).
	const (
		envAccessKey = "MINIO_ACCESS_KEY"
		envSecretKey = "MINIO_SECRET_KEY"
	)
	grants := []vault.GrantRow{
		{EnvVarName: envAccessKey, SecretPath: cfg.MinIOAccessKeyPath},
		{EnvVarName: envSecretKey, SecretPath: cfg.MinIOSecretKeyPath},
	}
	secrets, err := vaultClient.Fetch(ctx, grants)
	if err != nil {
		logger.Error("objstore: vault fetch for MinIO credentials failed; aborting startup", "err", err)
		return nil, ExitFailure
	}
	defer func() {
		for _, sv := range secrets {
			sv.Zero()
		}
	}()

	accessSV, ok := secrets[envAccessKey]
	if !ok {
		logger.Error("objstore: vault returned no value for MinIO access key")
		return nil, ExitFailure
	}
	secretSV, ok := secrets[envSecretKey]
	if !ok {
		logger.Error("objstore: vault returned no value for MinIO secret key")
		return nil, ExitFailure
	}

	// UnsafeBytes is one of the two production call sites M2.3 documents
	// as legitimate (the other is internal/spawn env injection).
	client, err := objstore.New(objstore.Config{
		Endpoint:  cfg.MinIOEndpoint,
		UseTLS:    cfg.MinIOUseTLS,
		AccessKey: string(accessSV.UnsafeBytes()),
		SecretKey: string(secretSV.UnsafeBytes()),
		Bucket:    cfg.MinIOBucket,
		CompanyID: uuidString(cfg.CustomerID()),
	}, logger)
	if err != nil {
		logger.Error("objstore: client construction failed", "err", err)
		return nil, ExitFailure
	}

	// Bootstrap is fail-closed (mirrors M2.2 mempalace-bootstrap).
	bootstrapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := client.BootstrapBucket(bootstrapCtx); err != nil {
		logger.Error("objstore: bootstrap failed; aborting startup",
			"err", err, "endpoint", cfg.MinIOEndpoint, "bucket", cfg.MinIOBucket)
		return nil, ExitFailure
	}

	return client, ExitOK
}

// buildDashboardAPIServer wires the dashboardapi.Server with the route
// set + auth middleware. Returns nil when objstore wiring was skipped
// (M2.1/M2.2 chaos test path) — runDaemon's caller adapts.
func buildDashboardAPIServer(
	cfg *config.Config,
	pool *pgxpool.Pool,
	objstoreClient *objstore.Client,
	sharedPalaceClient *mempalace.Client,
	logger *slog.Logger,
) *dashboardapi.Server {
	if objstoreClient == nil {
		return nil
	}

	// QueryClient: built directly from cfg so it lights up even when
	// `sharedPalaceClient` is nil (GARRISON_DISABLE_PALACE_BOOTSTRAP=1
	// path used by M2.1/M2.2 chaos tests + the dev stack). The
	// QueryClient only needs the docker-exec config + a reachable
	// mempalace container — it does NOT depend on bootstrap output.
	// Earlier revisions tied creation to sharedPalaceClient, which made
	// the /api/mempalace/recent-writes + /recent-kg routes 404 in any
	// environment that skipped bootstrap.
	queryClient := mempalace.NewQueryClient(
		dockerexec.RealDockerExec{DockerBin: cfg.DockerBin},
		cfg.MempalaceContainer,
		cfg.PalacePath,
		logger,
	)
	queryClient.DockerHost = cfg.DockerHost
	_ = sharedPalaceClient // retained as a parameter for future wiring; see comment above

	// SessionValidator: closure over pool.QueryRow against the dashboard's
	// better-auth `sessions` table. Cookie value is the `token` column;
	// supervisor connects as schema owner and already has SELECT (per
	// T001 reality-adjustment, no GRANT migration needed).
	sessionRowQuery := dashboardapi.SessionRowQuery(
		func(ctx context.Context, token string) (string, time.Time, error) {
			var userID pgtype.UUID
			var expiresAt time.Time
			err := pool.QueryRow(ctx,
				`SELECT user_id, expires_at FROM sessions WHERE token = $1`,
				token,
			).Scan(&userID, &expiresAt)
			if err != nil {
				return "", time.Time{}, err
			}
			return uuidString(userID), expiresAt, nil
		},
	)
	validator := dashboardapi.NewSQLSessionValidator(sessionRowQuery, time.Now)

	deps := dashboardapi.Deps{
		Objstore:         objstoreClient,
		Mempalace:        queryClient,
		SessionValidator: validator,
		Logger:           logger,
		CompanyID:        uuidString(cfg.CustomerID()),
	}
	srv := dashboardapi.NewServer(cfg, deps)
	if err := srv.RegisterDefaultRoutes(deps); err != nil {
		logger.Error("dashboardapi: route registration failed", "err", err)
		return nil
	}
	return srv
}

// buildVaultClient constructs the M2.3 vault.Fetcher when the Infisical
// address is configured. Returns (nil, ExitOK) when vault should be
// skipped (fake-agent path or no Infisical address). On Infisical
// failures the supervisor must abort startup — returns (nil,
// ExitFailure).
func buildVaultClient(ctx context.Context, cfg *config.Config, logger *slog.Logger) (vault.Fetcher, int) {
	if cfg.UseFakeAgent || cfg.InfisicalAddr() == "" {
		return nil, ExitOK
	}
	cid := cfg.CustomerID()
	cidStr := fmt.Sprintf("%x-%x-%x-%x-%x",
		cid.Bytes[0:4], cid.Bytes[4:6], cid.Bytes[6:8], cid.Bytes[8:10], cid.Bytes[10:16])
	vc, err := vault.NewClient(ctx, vault.ClientConfig{
		SiteURL:      cfg.InfisicalAddr(),
		ClientID:     cfg.InfisicalClientID(),
		ClientSecret: cfg.InfisicalClientSecret(),
		ProjectID:    cfg.InfisicalProjectID(),
		Environment:  cfg.InfisicalEnvironment(),
		CustomerID:   cidStr,
		Logger:       logger,
	})
	if err != nil {
		logger.Error("vault: failed to create Infisical client; aborting startup", "err", err)
		return nil, ExitFailure
	}
	return vc, ExitOK
}
