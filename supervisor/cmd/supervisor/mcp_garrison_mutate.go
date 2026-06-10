package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/garrison-hq/garrison/supervisor/internal/garrisonmutate"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runMCPGarrisonMutate is the `supervisor mcp garrison-mutate`
// subcommand entry point. Mirrors runMCPFinalize in shape: reads env
// vars, opens a pool, speaks JSON-RPC over stdio, exits cleanly on
// stdin EOF or SIGTERM.
//
// Required env:
//   - GARRISON_DATABASE_URL  (supervisor's main DSN; the verb handlers
//     write to chat_mutation_audit, tickets,
//     agents, hiring_proposals)
//
// plus exactly one caller anchor:
//   - GARRISON_CHAT_SESSION_ID + GARRISON_CHAT_MESSAGE_ID — chat mode
//     (M5.3; injected by mcpconfig.BuildChatConfig), full verb set.
//   - GARRISON_AGENT_INSTANCE_ID — agent mode (M8 FR-005; injected by
//     mcpconfig.Write for ticket spawns), create_ticket only, audit
//     rows anchor on agent_instance_id (FR-401).
func runMCPGarrisonMutate() int {
	dsn := os.Getenv("GARRISON_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp garrison-mutate: GARRISON_DATABASE_URL is required")
		return ExitUsage
	}
	sessionIDText := os.Getenv("GARRISON_CHAT_SESSION_ID")
	messageIDText := os.Getenv("GARRISON_CHAT_MESSAGE_ID")
	agentInstanceIDText := os.Getenv("GARRISON_AGENT_INSTANCE_ID")
	if agentInstanceIDText == "" && (sessionIDText == "" || messageIDText == "") {
		fmt.Fprintln(os.Stderr, "supervisor mcp garrison-mutate: caller anchor required: GARRISON_CHAT_SESSION_ID+GARRISON_CHAT_MESSAGE_ID or GARRISON_AGENT_INSTANCE_ID")
		return ExitUsage
	}

	var sessionID, messageID, agentInstanceID pgtype.UUID
	if sessionIDText != "" {
		if err := sessionID.Scan(sessionIDText); err != nil {
			fmt.Fprintf(os.Stderr, "supervisor mcp garrison-mutate: invalid GARRISON_CHAT_SESSION_ID: %v\n", err)
			return ExitUsage
		}
	}
	if messageIDText != "" {
		if err := messageID.Scan(messageIDText); err != nil {
			fmt.Fprintf(os.Stderr, "supervisor mcp garrison-mutate: invalid GARRISON_CHAT_MESSAGE_ID: %v\n", err)
			return ExitUsage
		}
	}
	if agentInstanceIDText != "" {
		if err := agentInstanceID.Scan(agentInstanceIDText); err != nil {
			fmt.Fprintf(os.Stderr, "supervisor mcp garrison-mutate: invalid GARRISON_AGENT_INSTANCE_ID: %v\n", err)
			return ExitUsage
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("stream", "garrison-mutate")
	logger.Info("garrison-mutate: subcommand starting",
		"chat_session_id", sessionIDText,
		"chat_message_id", messageIDText,
		"agent_instance_id", agentInstanceIDText,
	)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("garrison-mutate: pool init failed", "err", err)
		return ExitFailure
	}
	defer pool.Close()

	// Serve owns the exactly-one-anchor validation (both-set and
	// neither-set are rejected there).
	if err := garrisonmutate.Serve(ctx, os.Stdin, os.Stdout, garrisonmutate.Deps{
		Pool:            pool,
		ChatSessionID:   sessionID,
		ChatMessageID:   messageID,
		AgentInstanceID: agentInstanceID,
		Logger:          logger,
	}); err != nil {
		logger.Error("garrison-mutate: Serve returned error", "err", err)
		return ExitFailure
	}
	return ExitOK
}
