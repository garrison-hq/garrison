package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/garrison-hq/garrison/supervisor/internal/pgmcp"
)

// runMCPPostgres delegates stdin/stdout to the in-tree Postgres MCP server.
// This is the subprocess Claude Code spawns via --mcp-config: it speaks
// JSON-RPC 2.0 over stdio against a read-only Postgres connection bound
// to the garrison_agent_ro role, and exits when Claude closes stdin or
// the supervisor signals the process group.
//
// The DSN arrives via env (GARRISON_PGMCP_DSN), not argv, so it never
// appears in `ps` output (NFR-111). The supervisor composes the DSN in
// config.AgentRODSN() and writes it into the mcp-config-<uuid>.json
// file under the env key — mcpconfig.Write owns that write path.
//
// Exit codes: ExitOK on clean exit (EOF on stdin or ctx cancel); ExitUsage
// when GARRISON_PGMCP_DSN is missing; ExitFailure on a Serve-time error.
func runMCPPostgres() int {
	dsn := os.Getenv("GARRISON_PGMCP_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp-postgres: GARRISON_PGMCP_DSN is required")
		return ExitUsage
	}

	// The supervisor sends SIGTERM to the process group on shutdown or
	// bail; Serve returns cleanly when ctx is cancelled, giving it a
	// chance to flush any in-flight response before the group-level
	// SIGKILL escalation lands.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("pgmcp: starting", "stream", "pgmcp")

	// GARRISON_PGMCP_PID_FILE is a test-only hook (T018 chaos test
	// "pgmcp dies mid-run" uses it to discover the subprocess PID so
	// it can externally kill it). Production never sets it; failure
	// to write is warned and otherwise ignored.
	if pidFile := os.Getenv("GARRISON_PGMCP_PID_FILE"); pidFile != "" {
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
			logger.Warn("pgmcp: GARRISON_PGMCP_PID_FILE write failed", "stream", "pgmcp", "err", err)
		}
	}

	if err := pgmcp.Serve(ctx, os.Stdin, os.Stdout, dsn); err != nil {
		logger.Error("pgmcp: Serve returned error", "stream", "pgmcp", "err", err)
		return ExitFailure
	}
	return ExitOK
}
