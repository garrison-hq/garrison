package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// installSignalHandler registers a process-wide Notify for SIGTERM, SIGINT,
// and SIGHUP and returns the channel plus a stop func. It must be called
// before any long-running operation that wants a chance to observe these
// signals — notably before pgdb.Connect and pgdb.AcquireAdvisoryLock, which
// can block. Once signal.Notify has run the Go runtime stops terminating the
// process on these signals, so even work that ignores sigCh will no longer
// die with exit 143; the signal sits in the channel buffer until consumed.
func installSignalHandler() (<-chan os.Signal, func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	return sigCh, func() { signal.Stop(sigCh) }
}

// watchSignals blocks until ctx is done or one of the registered signals
// arrives on sigCh. On signal, it calls cancel() to cascade shutdown
// through the errgroup and logs which signal fired (contracts/cli.md
// §"Signals").
//
// SIGHUP is treated identically to SIGTERM in M1; the plan-level open
// question on SIGHUP semantics is resolved here as "graceful shutdown, not
// config-reload" because M1 has no reloadable config to speak of (env vars
// only apply at process start).
func watchSignals(ctx context.Context, sigCh <-chan os.Signal, cancel context.CancelFunc, logger *slog.Logger) {
	select {
	case <-ctx.Done():
		return
	case sig := <-sigCh:
		logger.Info("shutdown signal received; cancelling root context", "signal", sig.String())
		cancel()
	}
}
