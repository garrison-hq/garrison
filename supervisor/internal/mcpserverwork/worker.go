// Package mcpserverwork is the reactive worker that consumes the
// `work.mcp_server.registration_requested` pg_notify channel emitted
// by the M8 INSERT trigger on mcp_servers. For each event it:
//
//  1. Reads the mcp_servers row by id (the pg_notify payload's event_id
//     duplicates the row id so the M1 dispatcher envelope accepts it).
//  2. Fetches any upstream bearer token from Infisical at the row's
//     bearer_token_path (operator-supplied; optional).
//  3. Calls mcpjungle.Client.RegisterServer with the resolved spec.
//  4. UPDATEs the row to status='registered' on success or
//     status='failed' + failure_reason on error.
//  5. Writes the canonical chat_mutation_audit row anchored to the
//     resolved final outcome (FR-306 single-row invariant).
//
// M8 ships single-attempt semantics — a failed registration stays
// failed until the operator re-submits via the dashboard form. This
// keeps the worker idempotent and avoids the retry-storm shape that
// hurt the M7 spike (see specs/_context/m8-context.md "what this
// milestone is NOT").
//
// The worker is errgroup-managed (cmd/supervisor/main.go owns the
// lifecycle): SIGTERM cancels the context, the in-flight Dispatch
// drains, Run returns nil.
package mcpserverwork

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpjungle"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// VaultFetcher is the narrow seam the worker uses to look up the
// upstream bearer token for an MCP server registration (if the
// operator supplied one). *vault.Client.Fetch satisfies it via a small
// adapter; tests inject a fake.
type VaultFetcher interface {
	FetchOne(ctx context.Context, path string) (string, error)
}

// Deps wires the worker's collaborators. Constructed at supervisor boot
// in cmd/supervisor/main.go.
type Deps struct {
	Queries *store.Queries
	Client  *mcpjungle.Client
	Vault   VaultFetcher // may be nil if no row in the registry has a bearer_token_path
	Logger  *slog.Logger
}

// Worker is the per-channel handler registered with the M1 dispatcher.
// It is stateless apart from Deps; the dispatcher's in-flight dedupe
// guards against double-fire from LISTEN + poll.
type Worker struct {
	deps Deps
}

// New constructs a Worker. Deps.Queries and Deps.Client are required;
// Deps.Vault and Deps.Logger are optional (nil Logger falls back to
// slog.Default; nil Vault means any row with a non-nil bearer_token_path
// surfaces an error to the handler — operator-fixable).
func New(deps Deps) (*Worker, error) {
	if deps.Queries == nil {
		return nil, errors.New("mcpserverwork: Queries required")
	}
	if deps.Client == nil {
		return nil, errors.New("mcpserverwork: Client required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Worker{deps: deps}, nil
}

// Channel is the pg_notify channel the worker listens on.
const Channel = "work.mcp_server.registration_requested"

// Handle is the events.Handler the worker registers with the
// dispatcher. eventID is the mcp_servers.id (the trigger duplicates it
// from row.id into the M1 envelope so the dispatcher accepts the
// payload).
func (w *Worker) Handle(ctx context.Context, eventID pgtype.UUID) error {
	row, err := w.deps.Queries.GetMcpServerByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("mcpserverwork: GetMcpServerByID %s: %w", eventID, err)
	}
	if row.Status != "pending" {
		w.deps.Logger.Debug("mcpserverwork: row not pending, skipping",
			"mcp_server_id", uuidString(eventID), "status", row.Status)
		return nil
	}
	return w.process(ctx, row)
}

// process runs the registration state machine for one row. On any
// MCPJungle API error or vault failure the row transitions to 'failed'
// + failure_reason; on success the row transitions to 'registered' +
// registered_at=NOW(). Either way, a single chat_mutation_audit row
// lands (verb='register_mcp_server', outcome='success' | 'failed').
func (w *Worker) process(ctx context.Context, row store.McpServer) error {
	logger := w.deps.Logger.With("mcp_server_id", uuidString(row.ID), "name", row.Name)

	spec := mcpjungle.ServerSpec{
		Name:      row.Name,
		Transport: row.Transport,
	}
	if row.Url != nil {
		spec.URL = *row.Url
	}
	if row.BearerTokenPath != nil && *row.BearerTokenPath != "" {
		if w.deps.Vault == nil {
			return w.fail(ctx, row, "vault fetcher not configured but bearer_token_path is set")
		}
		token, err := w.deps.Vault.FetchOne(ctx, *row.BearerTokenPath)
		if err != nil {
			return w.fail(ctx, row, fmt.Sprintf("vault fetch %s: %v", *row.BearerTokenPath, err))
		}
		spec.BearerToken = token
	}

	if _, err := w.deps.Client.RegisterServer(ctx, spec); err != nil {
		return w.fail(ctx, row, fmt.Sprintf("MCPJungle RegisterServer: %v", err))
	}

	if err := w.markRegistered(ctx, row); err != nil {
		logger.Error("mcpserverwork: mark-registered failed", "err", err)
		return err
	}
	logger.Info("mcpserverwork: registered MCP server")
	return nil
}

// fail transitions the row to status='failed' + failure_reason, writes
// the audit row with outcome='failed', and returns nil (the worker's
// goroutine continues; M8 ships no retries).
func (w *Worker) fail(ctx context.Context, row store.McpServer, reason string) error {
	logger := w.deps.Logger.With("mcp_server_id", uuidString(row.ID), "name", row.Name)
	logger.Warn("mcpserverwork: registration failed", "reason", reason)

	reasonPtr := reason
	if err := w.deps.Queries.UpdateMcpServerStatus(ctx, store.UpdateMcpServerStatusParams{
		Status:        "failed",
		FailureReason: &reasonPtr,
		ID:            row.ID,
	}); err != nil {
		return fmt.Errorf("mcpserverwork: UpdateMcpServerStatus(failed): %w", err)
	}
	return w.writeAudit(ctx, row, "failed", reason)
}

func (w *Worker) markRegistered(ctx context.Context, row store.McpServer) error {
	var nilReason *string
	if err := w.deps.Queries.UpdateMcpServerStatus(ctx, store.UpdateMcpServerStatusParams{
		Status:        "registered",
		FailureReason: nilReason,
		ID:            row.ID,
	}); err != nil {
		return fmt.Errorf("mcpserverwork: UpdateMcpServerStatus(registered): %w", err)
	}
	return w.writeAudit(ctx, row, "success", "")
}

func (w *Worker) writeAudit(ctx context.Context, row store.McpServer, outcome, failureDetail string) error {
	args := fmt.Sprintf(`{"name":%q,"transport":%q,"customer_slug":%q,"outcome_detail":%q}`,
		row.Name, row.Transport, row.CustomerSlug, failureDetail)
	resourceID := uuidString(row.ID)
	resourceType := "mcp_server"
	// The audit row has both chat_session_id and agent_instance_id
	// NULL — the worker is supervisor-side, anchored to the
	// mcp_servers row itself via affected_resource_id. M7+ schema
	// already permits both anchors NULL.
	var nilUUID pgtype.UUID
	_, err := w.deps.Queries.InsertAgentAnchoredAudit(ctx, store.InsertAgentAnchoredAuditParams{
		ChatSessionID:        nilUUID,
		ChatMessageID:        nilUUID,
		AgentInstanceID:      nilUUID,
		Verb:                 "register_mcp_server",
		ArgsJsonb:            []byte(args),
		Outcome:              outcome,
		ReversibilityClass:   2,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &resourceType,
	})
	if err != nil {
		return fmt.Errorf("mcpserverwork: InsertAgentAnchoredAudit(%s): %w", outcome, err)
	}
	return nil
}

// uuidString formats a pgtype.UUID as canonical 36-char hex. Same shape
// as the helper in internal/mcpjungle.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
