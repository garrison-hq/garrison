// Package actionbroker — dispatcher.go
//
// RunLoop drives the poll-fallback ticker for the M11 action broker:
// every Deps.PollInterval it claims any unclaimed dispatchable rows and
// feeds them through Handle. The LISTEN path (registered in
// cmd/supervisor/main.go via workerExtras[Channel] = worker.Handle)
// provides low-latency delivery; the ticker is the at-least-once
// backstop for missed notifies.
//
// Handle implements the per-row state machine (plan §"Thread 3", D10):
//
//  1. ClaimDispatchablePendingAction — FOR UPDATE SKIP LOCKED; if no
//     row claims nothing (second claim is a no-op — SC-006).
//  2. Tier-specific dispatchability gate: auto/notify rows are
//     dispatchable from status='pending'; approve rows require
//     status='approved'; human_only rows are structurally excluded by
//     the claim query but the in-Handle guard is defence-in-depth.
//  3. Vault fetch — fail-closed: if the vault is unavailable the row
//     transitions to 'failed' and no external call is made (FR-023).
//  4. External provider call (GitHub.PostComment).
//  5. On success: MarkPendingActionExecuted + 'executed' outcome; notify
//     tier additionally gets a 'notified' outcome (D17).
//  6. On recoverable failure: MarkPendingActionFailed + 'failed' outcome;
//     no auto-retry (FR-022/D12).
package actionbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the pg_notify channel the dispatcher listens on (plan D9,
// FR-018, D18). The dot-delimited convention matches the M8/M9/M10
// channels ("work.mcp_server.registration_requested", etc.).
// garrisonmutate/verbs_actions.go references the same string constant
// via its own local copy to avoid a circular import.
const Channel = "work.action.dispatch_requested"

// VaultFetcher is the narrow seam used to fetch the GitHub PAT from
// Infisical (plan D11, FR-019). *vault.Client satisfies this interface.
// Tests inject a fake.
type VaultFetcher interface {
	Fetch(ctx context.Context, req []vault.GrantRow) (map[string]vault.SecretValue, error)
}

// GitHubPoster is the seam for the external provider call (plan D1, D11,
// FR-020). github.go's PostCommentClient satisfies it. Tests inject a
// fake so no real HTTP is needed in unit tests.
type GitHubPoster interface {
	// PostComment posts the comment body to the given target using the
	// provided PAT. Returns the comment HTML URL on success. Returns
	// ErrRecoverable for transient/rate-limit failures; returns a plain
	// error for terminal failures (404/422).
	PostComment(ctx context.Context, target Target, body, pat string) (commentURL string, err error)
}

// Deps wires the Worker's collaborators. Constructed at supervisor boot
// in cmd/supervisor/main.go after vault + pool are ready (the
// mcpserverwork.Worker build-site precedent).
type Deps struct {
	Pool    *pgxpool.Pool
	Queries *store.Queries
	// Vault fetches the GitHub PAT at dispatch time (one secret per
	// dispatch). The PAT vault path is configured via PATPath.
	Vault  VaultFetcher
	GitHub GitHubPoster
	Logger *slog.Logger
	// PATPath is the Infisical vault path for the GitHub PAT, e.g.
	// "actions/GITHUB_PAT". Sourced from cfg.ActionGitHubPATPath.
	PATPath string
	// PollInterval is the fallback ticker cadence (cfg.ActionPollInterval).
	// Used by RunLoop; ignored if zero (callers should always set it).
	PollInterval time.Duration
	// Now is a test seam for the current time. Defaults to time.Now when nil.
	Now func() time.Time
}

// Worker is the per-channel handle registered with the M1 dispatcher.
// It is stateless beyond Deps.
type Worker struct {
	deps Deps
}

// New constructs a Worker. Pool, Queries, Vault, and GitHub are required.
// Logger defaults to slog.Default() when nil.
func New(deps Deps) (*Worker, error) {
	if deps.Pool == nil {
		return nil, errors.New("actionbroker: Pool required")
	}
	if deps.Queries == nil {
		return nil, errors.New("actionbroker: Queries required")
	}
	if deps.Vault == nil {
		return nil, errors.New("actionbroker: Vault required")
	}
	if deps.GitHub == nil {
		return nil, errors.New("actionbroker: GitHub poster required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Worker{deps: deps}, nil
}

// RunLoop drives the poll-fallback ticker (plan D9, FR-018). On each
// tick it calls Handle with a zero UUID to trigger a claim-any path.
// Managed by main's errgroup; returns nil on context cancellation so a
// graceful shutdown does not poison sibling subsystems.
func RunLoop(ctx context.Context, deps Deps) error {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	interval := deps.PollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w := &Worker{deps: deps}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Zero UUID signals Handle to claim any dispatchable row
			// (poll-fallback path). A zero UUID payload is the at-
			// least-once backstop for missed pg_notify deliveries.
			var zero pgtype.UUID
			if err := w.Handle(ctx, zero); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				deps.Logger.Error("actionbroker: poll-tick Handle error", "error", err)
			}
		}
	}
}

// Handle is the events.Handler signature registered with the M1
// dispatcher on Channel. eventID is the pending_actions.id UUID from the
// pg_notify payload; a zero UUID triggers the claim-any poll-fallback
// path used by RunLoop.
//
// Per-row state machine (plan D10, FR-021):
//  1. Open a tx and claim one row via FOR UPDATE SKIP LOCKED.
//  2. Apply the tier-specific dispatchability gate inside the tx.
//  3. On vault failure: mark failed + outcome, commit, return nil.
//  4. Call the provider; on success mark executed + outcomes, commit.
//  5. On recoverable failure: mark failed + outcome, commit, no retry.
func (w *Worker) Handle(ctx context.Context, eventID pgtype.UUID) error {
	tx, err := w.deps.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("actionbroker: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := w.deps.Queries.WithTx(tx)

	// Claim one dispatchable row. FOR UPDATE SKIP LOCKED guarantees
	// at most one claimant per row across concurrent dispatchers
	// (SC-006). Returns pgx.ErrNoRows when nothing is claimable.
	row, err := q.ClaimDispatchablePendingAction(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Nothing to dispatch — normal idle state.
			return tx.Commit(ctx)
		}
		return fmt.Errorf("actionbroker: ClaimDispatchablePendingAction: %w", err)
	}

	logger := w.deps.Logger.With(
		"pending_action_id", uuidStr(row.ID),
		"action_type", row.ActionType,
		"tier", row.Tier,
		"status", row.Status,
	)

	// Tier-specific dispatchability gate (plan §"Thread 3", step 2).
	switch Tier(row.Tier) {
	case TierHumanOnly:
		// human_only rows are structurally excluded by the claim query
		// (ClaimDispatchablePendingAction filters tier <> 'human_only').
		// This branch is defence-in-depth; in normal operation it is
		// never reached through the LISTEN path (FR-017/US4#4).
		logger.Info("actionbroker: skipping human_only action")
		if err := q.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
			PendingActionID:   row.ID,
			AgentInstanceID:   row.AgentInstanceID,
			Outcome:           "skipped_human_only",
			Detail:            "human_only actions are never dispatched by the supervisor",
			StructuredOutcome: nil,
		}); err != nil {
			return fmt.Errorf("actionbroker: insert skipped_human_only outcome: %w", err)
		}
		return tx.Commit(ctx)

	case TierApprove:
		// approve-tier rows are dispatchable only when status='approved'
		// (the operator's approve Server Action has already fired). A row
		// still at status='pending' is not yet approved — skip it (US4#3).
		if row.Status != "approved" {
			logger.Debug("actionbroker: approve-tier row not yet approved, skipping",
				"status", row.Status)
			// Roll back to release the FOR UPDATE lock cleanly.
			_ = tx.Rollback(ctx)
			return nil
		}

	case TierAuto, TierNotify:
		// auto/notify rows are dispatched from status='pending'.
		// ClaimDispatchablePendingAction already checks status IN
		// ('pending','approved'), so a row at 'pending' is fine.

	default:
		// Unknown tier — log and skip without crashing the loop.
		logger.Error("actionbroker: unknown tier; skipping row", "tier", row.Tier)
		_ = tx.Rollback(ctx)
		return nil
	}

	// Fetch the vault-scoped GitHub PAT (plan D11, FR-019, FR-023).
	// Fail-closed: any vault error marks the row failed, no external
	// call is made, and the PAT never falls back to an agent-visible path.
	secrets, vaultErr := w.deps.Vault.Fetch(ctx, []vault.GrantRow{
		{EnvVarName: "GITHUB_PAT", SecretPath: w.deps.PATPath},
	})
	if vaultErr != nil {
		logger.Warn("actionbroker: vault fetch failed; marking row failed",
			"path", w.deps.PATPath, "error", vaultErr)
		return w.failRow(ctx, q, tx, row, "vault unavailable: "+vaultErr.Error())
	}
	patValue := secrets["GITHUB_PAT"]
	defer patValue.Zero() // vaultlog discipline: zero after use

	// Unmarshal the target JSONB from the pending_actions row.
	var target Target
	if err := json.Unmarshal(row.Target, &target); err != nil {
		logger.Error("actionbroker: cannot unmarshal target JSONB; marking failed",
			"error", err)
		return w.failRow(ctx, q, tx, row, "malformed target JSONB: "+err.Error())
	}

	// External provider call (FR-020, FR-008). The PAT is passed as a
	// plain string directly to the HTTP header — it never touches any
	// logger or structured field (vaultlog discipline, SC-005).
	commentURL, postErr := w.deps.GitHub.PostComment(
		ctx, target, row.RenderedPayload,
		string(patValue.UnsafeBytes()),
	)

	if postErr != nil {
		if errors.Is(postErr, ErrRecoverable) {
			// Recoverable failure (5xx/429): mark failed, no retry
			// (FR-022/D12). The row surfaces in the Outbox for
			// operator-initiated re-request.
			logger.Warn("actionbroker: recoverable provider failure; marking row failed",
				"error", postErr)
			return w.failRow(ctx, q, tx, row, postErr.Error())
		}
		// Terminal failure (404/422): also mark failed, no retry.
		logger.Error("actionbroker: terminal provider failure; marking row failed",
			"error", postErr)
		return w.failRow(ctx, q, tx, row, postErr.Error())
	}

	// Success path — build the structured outcome JSON.
	structuredJSON, _ := json.Marshal(map[string]string{
		"comment_url": commentURL,
	})

	// Resolve the current time for dispatched_at.
	now := time.Now
	if w.deps.Now != nil {
		now = w.deps.Now
	}

	if err := q.MarkPendingActionExecuted(ctx, store.MarkPendingActionExecutedParams{
		DispatchedAt: pgtype.Timestamptz{Time: now().UTC(), Valid: true},
		ID:           row.ID,
	}); err != nil {
		return fmt.Errorf("actionbroker: MarkPendingActionExecuted: %w", err)
	}

	if err := q.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   row.ID,
		AgentInstanceID:   row.AgentInstanceID,
		Outcome:           "executed",
		Detail:            commentURL,
		StructuredOutcome: structuredJSON,
	}); err != nil {
		return fmt.Errorf("actionbroker: insert executed outcome: %w", err)
	}

	// notify-tier additionally gets a 'notified' outcome row so the
	// Outbox can distinguish post-hoc feed items from pending approvals
	// (plan D17, US4#2, FR-028).
	if Tier(row.Tier) == TierNotify {
		if err := q.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
			PendingActionID:   row.ID,
			AgentInstanceID:   row.AgentInstanceID,
			Outcome:           "notified",
			Detail:            "operator notified post-hoc via Outbox feed",
			StructuredOutcome: nil,
		}); err != nil {
			return fmt.Errorf("actionbroker: insert notified outcome: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("actionbroker: commit executed: %w", err)
	}

	logger.Info("actionbroker: dispatched action",
		"comment_url", commentURL,
		"tier", row.Tier,
	)
	return nil
}

// failRow marks the pending_actions row as 'failed', writes a 'failed'
// outcome to the immutable history, and commits. Returns nil on success
// so the dispatcher loop continues; the row surfaces in the Outbox for
// operator-initiated re-request (FR-022/FR-023).
func (w *Worker) failRow(
	ctx context.Context,
	q *store.Queries,
	tx pgx.Tx,
	row store.PendingAction,
	detail string,
) error {
	if err := q.MarkPendingActionFailed(ctx, row.ID); err != nil {
		return fmt.Errorf("actionbroker: MarkPendingActionFailed: %w", err)
	}
	if err := q.InsertPendingActionOutcome(ctx, store.InsertPendingActionOutcomeParams{
		PendingActionID:   row.ID,
		AgentInstanceID:   row.AgentInstanceID,
		Outcome:           "failed",
		Detail:            detail,
		StructuredOutcome: nil,
	}); err != nil {
		return fmt.Errorf("actionbroker: insert failed outcome: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("actionbroker: commit failed outcome: %w", err)
	}
	return nil
}

// uuidStr formats a pgtype.UUID as the canonical 36-char dash-delimited
// hex string. Used only for logging (the PAT is never logged here).
func uuidStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	var buf bytes.Buffer
	for i, b := range u.Bytes {
		switch i {
		case 4, 6, 8, 10:
			buf.WriteByte('-')
		}
		fmt.Fprintf(&buf, "%02x", b)
	}
	return buf.String()
}
