//go:build integration

// M5.2 — RunRestartSweep orphan-operator-row pass tests (T002).
//
// The orphan pass detects sessions where the newest chat_messages row
// is role='operator', older than the 60s cutoff, with no assistant pair
// at turn_index+1. For each match the sweep synthesises an aborted
// assistant row + flips the session to status='aborted'. These tests
// exercise the in-package RunRestartSweep entry point against a real
// testcontainer Postgres so the sqlc query, the WithoutCancel grace
// path, and the inter-pass independence all run end-to-end.

package chat

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// minimalSweepDeps wires only the fields RunRestartSweep reads. Vault /
// docker / image config are unused by the sweep; leaving them zero-valued
// surfaces if the sweep ever grows an unexpected dependency.
func minimalSweepDeps(pool *pgxpool.Pool, q *store.Queries) Deps {
	return Deps{
		Pool:               pool,
		Queries:            q,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		TerminalWriteGrace: 5 * time.Second,
	}
}

// seedOrphanOperator inserts an operator chat_messages row + backdates
// its created_at by ageSecs seconds so it qualifies as orphaned past the
// 60s sweep cutoff (when ageSecs > 60). Returns the parent session id +
// the operator turn index for follow-up assertions.
func seedOrphanOperator(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *store.Queries, ageSecs int) (sessionID pgtype.UUID, opTurn int32) {
	t.Helper()
	sess, err := q.CreateChatSession(ctx, newUUID(t))
	if err != nil {
		t.Fatalf("CreateChatSession: %v", err)
	}
	op, err := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID,
		Content:   ptrFn("orphan candidate"),
	})
	if err != nil {
		t.Fatalf("InsertOperatorMessage: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - make_interval(secs => $1) WHERE id = $2",
		ageSecs, op.ID); err != nil {
		t.Fatalf("backdate operator row: %v", err)
	}
	return sess.ID, op.TurnIndex
}

// findSyntheticAssistant returns (status, errorKind, content, ok) for
// the assistant row at the supplied turn_index, or ok=false if no row
// exists. Used to assert the sweep wrote a synthetic-aborted row.
func findSyntheticAssistant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID pgtype.UUID, turnIndex int32) (status, errorKind, content string, ok bool) {
	t.Helper()
	row := pool.QueryRow(ctx,
		`SELECT status,
		        COALESCE(error_kind, ''),
		        COALESCE(content, '')
		   FROM chat_messages
		  WHERE session_id = $1 AND turn_index = $2 AND role = 'assistant'
		  LIMIT 1`,
		sessionID, turnIndex)
	if err := row.Scan(&status, &errorKind, &content); err != nil {
		return "", "", "", false
	}
	return status, errorKind, content, true
}

// TestRunRestartSweep_OrphanOperatorRowSyntheticAssistant — the happy
// path: an active session with an old operator row and no assistant
// pair gets a synthesised aborted assistant row at turn_index+1 plus
// chat_sessions.status='aborted'. Verifies the literal content +
// error_kind + role + status pinning per FR-291.
func TestRunRestartSweep_OrphanOperatorRowSyntheticAssistant(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessID, opTurn := seedOrphanOperator(t, ctx, pool, q, 120)

	if err := RunRestartSweep(ctx, minimalSweepDeps(pool, q)); err != nil {
		t.Fatalf("RunRestartSweep: %v", err)
	}

	st, ek, content, ok := findSyntheticAssistant(t, ctx, pool, sessID, opTurn+1)
	if !ok {
		t.Fatalf("synthetic assistant row missing at turn_index=%d", opTurn+1)
	}
	if st != "aborted" {
		t.Errorf("synthetic.status=%q; want aborted", st)
	}
	if ek != ErrorSupervisorRestart {
		t.Errorf("synthetic.error_kind=%q; want %s", ek, ErrorSupervisorRestart)
	}
	if content != orphanSyntheticContent {
		t.Errorf("synthetic.content=%q; want %q", content, orphanSyntheticContent)
	}

	updated, err := q.GetChatSession(ctx, sessID)
	if err != nil {
		t.Fatalf("GetChatSession: %v", err)
	}
	if updated.Status != "aborted" {
		t.Errorf("session.status=%q; want aborted", updated.Status)
	}
	if !updated.EndedAt.Valid {
		t.Errorf("session.ended_at not set; want NOW()")
	}
}

// TestRunRestartSweep_OrphanIgnoresRecentOperatorRow — the cutoff
// boundary: an operator row aged 30s (< 60s) is NOT swept. The session
// stays 'active' and no assistant row is created. Pins the conservative
// cutoff against false-positives during normal supervisor spawn.
func TestRunRestartSweep_OrphanIgnoresRecentOperatorRow(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessID, opTurn := seedOrphanOperator(t, ctx, pool, q, 30)

	if err := RunRestartSweep(ctx, minimalSweepDeps(pool, q)); err != nil {
		t.Fatalf("RunRestartSweep: %v", err)
	}

	if _, _, _, ok := findSyntheticAssistant(t, ctx, pool, sessID, opTurn+1); ok {
		t.Errorf("synthetic assistant created for recent operator row; want untouched")
	}
	updated, _ := q.GetChatSession(ctx, sessID)
	if updated.Status != "active" {
		t.Errorf("session.status=%q; want active (not aborted)", updated.Status)
	}
}

// TestRunRestartSweep_OrphanIgnoresAssistantPair — a session with both
// operator and assistant rows committed should not match the orphan
// query regardless of operator-row age. Pins the
// no-existing-pair-at-turn+1 invariant.
func TestRunRestartSweep_OrphanIgnoresAssistantPair(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sess, _ := q.CreateChatSession(ctx, newUUID(t))
	op, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: sess.ID, Content: ptrFn("paired"),
	})
	asst, _ := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: sess.ID, TurnIndex: op.TurnIndex + 1,
	})
	// Mark the assistant completed — pair is fully landed.
	if err := q.CommitAssistantTerminal(ctx, store.CommitAssistantTerminalParams{
		ID:      asst.ID,
		Status:  "completed",
		Content: ptrFn("response"),
	}); err != nil {
		t.Fatalf("CommitAssistantTerminal: %v", err)
	}
	// Backdate the operator row so age is >60s; the orphan pass should
	// still skip because of the pair.
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - make_interval(secs => 120) WHERE id = $1",
		op.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if err := RunRestartSweep(ctx, minimalSweepDeps(pool, q)); err != nil {
		t.Fatalf("RunRestartSweep: %v", err)
	}

	updated, _ := q.GetChatSession(ctx, sess.ID)
	if updated.Status != "active" {
		t.Errorf("session.status=%q; want active (paired turn must not be swept)", updated.Status)
	}
}

// TestRunRestartSweep_OrphanIgnoresEndedSession — a session at
// status='ended' (or 'aborted') is skipped even if it has an old
// orphan operator row. Pins the active-only filter on the query.
func TestRunRestartSweep_OrphanIgnoresEndedSession(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessID, opTurn := seedOrphanOperator(t, ctx, pool, q, 120)
	if _, err := pool.Exec(ctx,
		"UPDATE chat_sessions SET status = 'ended', ended_at = now() WHERE id = $1",
		sessID); err != nil {
		t.Fatalf("flip session ended: %v", err)
	}

	if err := RunRestartSweep(ctx, minimalSweepDeps(pool, q)); err != nil {
		t.Fatalf("RunRestartSweep: %v", err)
	}

	if _, _, _, ok := findSyntheticAssistant(t, ctx, pool, sessID, opTurn+1); ok {
		t.Errorf("synthetic assistant created for ended session; want skipped")
	}
}

// TestRunRestartSweep_OrphanWritesUnderWithoutCancel — under a parent
// context that's been cancelled, the orphan terminal write still
// commits within TerminalWriteGrace per AGENTS.md concurrency rule 6.
// Pins the WithoutCancel + grace pattern so a SIGTERM mid-sweep cannot
// leave the operator row dangling without an assistant pair.
func TestRunRestartSweep_OrphanWritesUnderWithoutCancel(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	parentCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sessID, opTurn := seedOrphanOperator(t, parentCtx, pool, q, 120)

	// Cancel the parent ctx but call RunRestartSweep with that same
	// cancelled ctx. The first pass observes ctx.Err() and short-
	// circuits cleanly; we then re-run from a fresh root ctx that re-
	// enters the orphan pass — which simulates the real shutdown path
	// where the second pass runs under WithoutCancel grace once the
	// orphans are detected. The synthetic write must commit either way.
	cancel()

	deps := minimalSweepDeps(pool, q)

	// Direct write via the helper — the orphan-finder runs against the
	// fresh ctx, the inner write runs under WithoutCancel(parentCtx)
	// grace. Verify the helper succeeds even though parentCtx is dead.
	freshCtx, freshCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer freshCancel()
	cutoff := time.Now().Add(-60 * time.Second)
	cutoffTS := pgtype.Timestamptz{}
	_ = cutoffTS.Scan(cutoff)
	orphans, err := q.FindOrphanedOperatorSessions(freshCtx, cutoffTS)
	if err != nil {
		t.Fatalf("FindOrphanedOperatorSessions: %v", err)
	}
	if len(orphans) == 0 {
		t.Fatalf("no orphans detected; seed didn't qualify")
	}
	for _, o := range orphans {
		if err := writeOrphanSyntheticTerminal(parentCtx, deps, o, deps.TerminalWriteGrace); err != nil {
			t.Fatalf("writeOrphanSyntheticTerminal under cancelled parent: %v", err)
		}
	}

	st, ek, _, ok := findSyntheticAssistant(t, freshCtx, pool, sessID, opTurn+1)
	if !ok {
		t.Fatalf("synthetic row missing — WithoutCancel grace failed under cancelled parent")
	}
	if st != "aborted" || ek != ErrorSupervisorRestart {
		t.Errorf("synthetic state status=%q ek=%q; want aborted/%s", st, ek, ErrorSupervisorRestart)
	}
}

// TestRunRestartSweep_BothPassesIndependent — seeds one pending-
// streaming row (M5.1 sweep target) AND one orphan-operator-row
// (M5.2 sweep target) in distinct sessions and asserts both pass
// outputs land in a single sweep invocation.
func TestRunRestartSweep_BothPassesIndependent(t *testing.T) {
	pool := testdb.Start(t)
	q := store.New(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Pending-streaming session (M5.1 pass target).
	pendingSess, _ := q.CreateChatSession(ctx, newUUID(t))
	pendingOp, _ := q.InsertOperatorMessage(ctx, store.InsertOperatorMessageParams{
		SessionID: pendingSess.ID, Content: ptrFn("pending"),
	})
	pendingAsst, _ := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: pendingSess.ID, TurnIndex: pendingOp.TurnIndex + 1,
	})
	if _, err := pool.Exec(ctx,
		"UPDATE chat_messages SET created_at = now() - interval '120 seconds' WHERE id = $1",
		pendingAsst.ID); err != nil {
		t.Fatalf("backdate pending: %v", err)
	}

	// Orphan-operator session (M5.2 pass target).
	orphanSessID, orphanOpTurn := seedOrphanOperator(t, ctx, pool, q, 120)

	if err := RunRestartSweep(ctx, minimalSweepDeps(pool, q)); err != nil {
		t.Fatalf("RunRestartSweep: %v", err)
	}

	// M5.1 pass — pending row aborted, session aborted.
	pendSt, pendEk, _ := findAssistantRow(t, ctx, pool, pendingSess.ID)
	if pendSt != "aborted" || pendEk != ErrorSupervisorRestart {
		t.Errorf("M5.1 pass: assistant row status=%q ek=%q; want aborted/%s",
			pendSt, pendEk, ErrorSupervisorRestart)
	}
	pendSess, _ := q.GetChatSession(ctx, pendingSess.ID)
	if pendSess.Status != "aborted" {
		t.Errorf("M5.1 pass: session.status=%q; want aborted", pendSess.Status)
	}

	// M5.2 pass — synthetic assistant row landed, session aborted.
	orphSt, orphEk, _, ok := findSyntheticAssistant(t, ctx, pool, orphanSessID, orphanOpTurn+1)
	if !ok || orphSt != "aborted" || orphEk != ErrorSupervisorRestart {
		t.Errorf("M5.2 pass: synthetic row missing or wrong state ok=%v status=%q ek=%q",
			ok, orphSt, orphEk)
	}
	orphSess, _ := q.GetChatSession(ctx, orphanSessID)
	if orphSess.Status != "aborted" {
		t.Errorf("M5.2 pass: session.status=%q; want aborted", orphSess.Status)
	}
}
