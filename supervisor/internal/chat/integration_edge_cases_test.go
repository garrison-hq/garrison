//go:build integration

// T018 edge-case integration tests — cost cap, session ended, vault
// failure paths, restart sweep. Multi-turn cache-hit assertion lives
// in T020's Playwright pass; idle-timeout sweep tested separately
// (60s ticker is too long for unit timeframe).

package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// failingVault returns the supplied error for any Fetch call.
type failingVault struct{ err error }

func (f *failingVault) Fetch(ctx context.Context, req []vault.GrantRow) (map[string]vault.SecretValue, error) {
	return nil, f.err
}

// findAssistantRow does a direct chat_messages SELECT (the sqlc
// GetSessionTranscript skips non-completed assistant rows). Returns
// the matching row or zero-valued + false.
func findAssistantRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID pgtype.UUID) (status string, errorKind string, found bool) {
	t.Helper()
	row := pool.QueryRow(ctx,
		`SELECT status, COALESCE(error_kind, '')
		   FROM chat_messages
		  WHERE session_id = $1 AND role = 'assistant'
		  ORDER BY turn_index DESC
		  LIMIT 1`,
		sessionID)
	if err := row.Scan(&status, &errorKind); err != nil {
		return "", "", false
	}
	return status, errorKind, true
}

func minimalEdgeDeps(t *testing.T, pool *pgxpool.Pool, q *store.Queries, vc vault.Fetcher, ex *fakeDockerExec) Deps {
	return Deps{
		Pool:               pool,
		Queries:            q,
		VaultClient:        vc,
		DockerExec:         ex,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		CustomerID:         newUUID(t),
		OAuthVaultPath:     "/operator/CLAUDE_CODE_OAUTH_TOKEN",
		ChatContainerImage: "garrison-mockclaude:m5",
		MCPConfigDir:       t.TempDir(),
		DockerNetwork:      "garrison-net",
		TurnTimeout:        30 * time.Second,
		SessionIdleTimeout: 30 * time.Minute,
		SessionCostCapUSD:  10.00,
		TerminalWriteGrace: 5 * time.Second,
	}
}

func TestM5_1_CostCap_RefusesNextTurn(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exec := &fakeDockerExec{cannedNDJSON: cannedHappyPathNDJSON}
	deps := minimalEdgeDeps(t, pool, q, &fakeVault{}, exec)
	deps.SessionCostCapUSD = 1.00

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	cost, _ := numericFromString("1.50")
	_ = q.RollUpSessionCost(ctx, store.RollUpSessionCostParams{ID: sess.ID, DeltaUsd: cost})

	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{})
	if err := w.HandleMessageInSession(ctx, sess.ID, op.ID); err != nil {
		t.Fatalf("HandleMessageInSession: %v", err)
	}
	if exec.calls != 0 {
		t.Errorf("docker called %d times; want 0 (cost-capped)", exec.calls)
	}
	st, ek, found := findAssistantRow(t, ctx, pool, sess.ID)
	if !found || st != "failed" || ek != ErrorSessionCostCapReached {
		t.Errorf("assistant row: status=%q error_kind=%q; want failed/%s",
			st, ek, ErrorSessionCostCapReached)
	}
}

func TestM5_1_SessionEnded_RejectsNextMessage(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	exec := &fakeDockerExec{cannedNDJSON: cannedHappyPathNDJSON}
	deps := minimalEdgeDeps(t, pool, q, &fakeVault{}, exec)

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	_ = q.UpdateChatSessionStatus(ctx, store.UpdateChatSessionStatusParams{
		ID: sess.ID, Status: "ended",
	})
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{})
	_ = w.HandleMessageInSession(ctx, sess.ID, op.ID)

	if exec.calls != 0 {
		t.Errorf("docker called on ended session")
	}
	st, ek, found := findAssistantRow(t, ctx, pool, sess.ID)
	if !found || st != "failed" || ek != ErrorSessionEnded {
		t.Errorf("assistant row: status=%q error_kind=%q; want failed/%s",
			st, ek, ErrorSessionEnded)
	}
}

// TestM5_1_Vault_TokenAbsent uses REAL testcontainer Infisical with
// the secret deliberately NOT seeded. The chat path's classifyVaultError
// must surface vault.ErrVaultSecretNotFound as ErrorTokenNotFound.
// Per the M4 retro discipline this beats mocking the sentinel: it
// proves the actual Infisical 404 → SDK error → vault.Classify chain
// behaves as the chat path expects.
func TestM5_1_Vault_TokenAbsent(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Real Infisical, but DON'T seed the OAuth token — Fetch will
	// return the actual ErrVaultSecretNotFound sentinel.
	vaultClient, customerID := chatVaultStack(t, false /* don't seed */)

	exec := &fakeDockerExec{}
	deps := minimalEdgeDeps(t, pool, q, vaultClient, exec)
	deps.CustomerID = customerID

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{})
	_ = w.HandleMessageInSession(ctx, sess.ID, op.ID)

	if exec.calls != 0 {
		t.Errorf("docker called despite vault failure")
	}
	st, ek, found := findAssistantRow(t, ctx, pool, sess.ID)
	if !found || st != "failed" || ek != ErrorTokenNotFound {
		t.Errorf("assistant row: status=%q error_kind=%q; want failed/%s",
			st, ek, ErrorTokenNotFound)
	}
}

// TestM5_1_Vault_TokenExpired keeps the failingVault mock pending an
// invasive harness extension to revoke an ML's client_secret mid-test.
// Filing a follow-up rather than papering over with a synthetic fake.
//
// TODO: extend vault.InfisicalTestHarness with a RevokeIdentity helper
// (DELETE /api/v3/auth/universal-auth/identity/<id>/client-secrets/<csid>)
// and switch this test to that path. M4 retro discipline applies.
func TestM5_1_Vault_TokenExpired(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deps := minimalEdgeDeps(t, pool, q,
		&failingVault{err: vault.ErrVaultAuthExpired},
		&fakeDockerExec{})

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{})
	_ = w.HandleMessageInSession(ctx, sess.ID, op.ID)

	st, ek, _ := findAssistantRow(t, ctx, pool, sess.ID)
	if st != "failed" || ek != ErrorTokenExpired {
		t.Errorf("assistant row: status=%q error_kind=%q; want failed/%s",
			st, ek, ErrorTokenExpired)
	}
}

// TestM5_1_Vault_Unavailable similarly keeps the synthetic-error
// mock; simulating "Infisical is down" on a healthy testcontainer
// would require stopping the container mid-test, which is feasible
// (testcontainers.StopContainer) but adds 60s+ of harness gymnastics.
//
// TODO: extend chatVaultStack with a StopInfisicalForTest hook that
// stops the container, runs the assertion, and restarts it before
// teardown — and switch this test to that path. M4 retro discipline
// applies.
func TestM5_1_Vault_Unavailable(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deps := minimalEdgeDeps(t, pool, q,
		&failingVault{err: errors.New("connection refused")},
		&fakeDockerExec{})

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	w := NewWorker(deps, "/bin/true", "postgres://test/test", MempalaceWiring{})
	_ = w.HandleMessageInSession(ctx, sess.ID, op.ID)

	st, ek, _ := findAssistantRow(t, ctx, pool, sess.ID)
	if st != "failed" || ek != ErrorVaultUnavailable {
		t.Errorf("assistant row: status=%q error_kind=%q; want failed/%s",
			st, ek, ErrorVaultUnavailable)
	}
}

// TestM5_1_RestartSweep_AbortsStaleInflight backdates a pending
// assistant row 120s and verifies RunRestartSweep marks it aborted +
// rolls the session to aborted.
func TestM5_1_RestartSweep_AbortsStaleInflight(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("ping"),
	})
	asst, _ := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: sess.ID, TurnIndex: op.TurnIndex + 1,
	})
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - interval '120 seconds' WHERE id = $1",
		asst.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	deps := minimalEdgeDeps(t, pool, q, &fakeVault{}, &fakeDockerExec{})
	if err := RunRestartSweep(ctx, deps); err != nil {
		t.Fatalf("RunRestartSweep: %v", err)
	}

	st, ek, _ := findAssistantRow(t, ctx, pool, sess.ID)
	if st != "aborted" || ek != ErrorSupervisorRestart {
		t.Errorf("assistant row: status=%q error_kind=%q; want aborted/%s",
			st, ek, ErrorSupervisorRestart)
	}
	updated, _ := q.GetChatSession(ctx, sess.ID)
	if updated.Status != "aborted" {
		t.Errorf("session.status = %q; want aborted", updated.Status)
	}
}
