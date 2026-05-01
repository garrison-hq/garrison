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
// Required env (injected by mcpconfig.BuildChatConfig at chat-message
// spawn time when ChatConfigParams.DatabaseURL/ChatSessionID/ChatMessageID
// are set):
//   - GARRISON_DATABASE_URL  (supervisor's main DSN; the verb handlers
//     write to chat_mutation_audit, tickets,
//     agents, hiring_proposals)
//   - GARRISON_CHAT_SESSION_ID (uuid; scopes audit rows)
//   - GARRISON_CHAT_MESSAGE_ID (uuid; scopes audit rows)
func runMCPGarrisonMutate() int {
	dsn := os.Getenv("GARRISON_DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp garrison-mutate: GARRISON_DATABASE_URL is required")
		return ExitUsage
	}
	sessionIDText := os.Getenv("GARRISON_CHAT_SESSION_ID")
	if sessionIDText == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp garrison-mutate: GARRISON_CHAT_SESSION_ID is required")
		return ExitUsage
	}
	messageIDText := os.Getenv("GARRISON_CHAT_MESSAGE_ID")
	if messageIDText == "" {
		fmt.Fprintln(os.Stderr, "supervisor mcp garrison-mutate: GARRISON_CHAT_MESSAGE_ID is required")
		return ExitUsage
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(sessionIDText); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor mcp garrison-mutate: invalid GARRISON_CHAT_SESSION_ID: %v\n", err)
		return ExitUsage
	}
	var messageID pgtype.UUID
	if err := messageID.Scan(messageIDText); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor mcp garrison-mutate: invalid GARRISON_CHAT_MESSAGE_ID: %v\n", err)
		return ExitUsage
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("stream", "garrison-mutate")
	logger.Info("garrison-mutate: subcommand starting",
		"chat_session_id", sessionIDText,
		"chat_message_id", messageIDText,
	)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("garrison-mutate: pool init failed", "err", err)
		return ExitFailure
	}
	defer pool.Close()

	if err := garrisonmutate.Serve(ctx, os.Stdin, os.Stdout, garrisonmutate.Deps{
		Pool:          pool,
		ChatSessionID: sessionID,
		ChatMessageID: messageID,
		Logger:        logger,
	}); err != nil {
		logger.Error("garrison-mutate: Serve returned error", "err", err)
		return ExitFailure
	}
	return ExitOK
}
