package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runMCPFinalize is the `supervisor mcp finalize` subcommand entry
// point. Mirrors runMCPPostgres (M2.1) in shape: reads env vars,
// opens a connection, speaks JSON-RPC over stdio, exits cleanly on
// stdin EOF or SIGTERM.
//
// Required env:
//   - GARRISON_AGENT_INSTANCE_ID (uuid; injected by the MCP config
//     writer per FR-256 / spec Clarification 2026-04-23 Q4)
//   - GARRISON_DATABASE_URL (read-only DSN; the handler queries
//     agent_instances for the already-committed check per FR-260)
func runMCPFinalize() int {
	instanceIDText := os.Getenv("GARRISON_AGENT_INSTANCE_ID")
	if instanceIDText == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp finalize: GARRISON_AGENT_INSTANCE_ID is required")
		return ExitUsage
	}
	dsn := os.Getenv("GARRISON_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp finalize: GARRISON_DATABASE_URL is required")
		return ExitUsage
	}

	var instanceID pgtype.UUID
	if err := instanceID.Scan(instanceIDText); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor mcp finalize: invalid GARRISON_AGENT_INSTANCE_ID: %v\n", err)
		return ExitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("stream", "finalize")
	logger.Info("finalize: subcommand starting", "agent_instance_id", instanceIDText)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("finalize: pool init failed", "err", err)
		return ExitFailure
	}
	defer pool.Close()

	if err := finalize.Serve(ctx, os.Stdin, os.Stdout, finalize.Deps{
		Pool:            pool,
		AgentInstanceID: instanceID,
		Logger:          logger,
	}); err != nil {
		logger.Error("finalize: Serve returned error", "err", err)
		return ExitFailure
	}
	return ExitOK
}
