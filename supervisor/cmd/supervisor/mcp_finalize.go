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
//
// Optional env (M9 mode switch):
//   - GARRISON_FINALIZE_MODE ("ticket" default | "oneshot"; selects
//     which single tool the server exposes per M9 FR-304)
//   - GARRISON_SCHEDULED_RUN_ID (uuid; required when mode=oneshot —
//     keys the oneshot double-commit guard, M9 FR-260 analog)
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

	mode := os.Getenv("GARRISON_FINALIZE_MODE")
	if mode == "" {
		mode = finalize.ModeTicket
	}
	var scheduledRunID pgtype.UUID
	switch mode {
	case finalize.ModeTicket:
		// M2.2.1 path; no extra env.
	case finalize.ModeOneshot:
		runIDText := os.Getenv("GARRISON_SCHEDULED_RUN_ID")
		if runIDText == "" {
			fmt.Fprintln(os.Stderr, "supervisor mcp finalize: GARRISON_SCHEDULED_RUN_ID is required in oneshot mode")
			return ExitUsage
		}
		if err := scheduledRunID.Scan(runIDText); err != nil {
			fmt.Fprintf(os.Stderr, "supervisor mcp finalize: invalid GARRISON_SCHEDULED_RUN_ID: %v\n", err)
			return ExitUsage
		}
	default:
		fmt.Fprintf(os.Stderr, "supervisor mcp finalize: invalid GARRISON_FINALIZE_MODE %q (want %q or %q)\n",
			mode, finalize.ModeTicket, finalize.ModeOneshot)
		return ExitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("stream", "finalize")
	logger.Info("finalize: subcommand starting", "agent_instance_id", instanceIDText, "mode", mode)

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
		Mode:            mode,
		ScheduledRunID:  scheduledRunID,
	}); err != nil {
		logger.Error("finalize: Serve returned error", "err", err)
		return ExitFailure
	}
	return ExitOK
}
