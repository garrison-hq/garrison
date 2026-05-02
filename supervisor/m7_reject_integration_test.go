//go:build integration

// M7 T019 (US4 slice) — reject path: operator rejects a pending
// proposal; the row persists with status='rejected' and the audit row
// carries the operator-typed reason. No agents row is written.

package supervisor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/garrisonmutate"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM7RejectProposalPersistsReasonAndNoAgentRowLands(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, hiring_proposals, chat_messages, chat_sessions,
		         agent_install_journal, agent_container_events, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID, deptID, sessionID, messageID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name) VALUES ('m7 reject co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-rej')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd) VALUES ($1, 'active', 0) RETURNING id`,
		pgtype.UUID{Valid: true, Bytes: [16]byte{0xee}}).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status, content) VALUES ($1, 0, 'operator', 'completed', 'hire') RETURNING id`,
		sessionID).Scan(&messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	deps := garrisonmutate.Deps{Pool: pool, ChatSessionID: sessionID, ChatMessageID: messageID}
	args, _ := json.Marshal(garrisonmutate.ProposeHireArgs{
		RoleTitle:       "growth-strategist",
		DepartmentSlug:  "engineering",
		JustificationMD: "scan flagged unverified bytes — operator should reject",
	})
	v := garrisonmutate.FindVerb("propose_hire")
	res, err := v.Handler(ctx, deps, args)
	if err != nil || !res.Success {
		t.Fatalf("propose_hire: %v / %+v", err, res)
	}
	var proposalID pgtype.UUID
	if err := proposalID.Scan(res.AffectedResourceID); err != nil {
		t.Fatalf("parse: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	auditID, err := garrisonmutate.RejectProposal(ctx, tx, proposalID,
		pgtype.UUID{Valid: true, Bytes: [16]byte{0xff}}, "coarse-scan flagged suspicious entrypoint")
	if err != nil {
		t.Fatalf("RejectProposal: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Row persists.
	q := store.New(pool)
	prop, err := q.GetHiringProposalByID(ctx, proposalID)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if prop.Status != "rejected" {
		t.Errorf("status = %q; want rejected", prop.Status)
	}
	if prop.RejectedReason == nil || !strings.Contains(*prop.RejectedReason, "coarse-scan") {
		t.Errorf("rejected_reason missing operator text: %v", prop.RejectedReason)
	}

	// Audit row.
	var verbCol string
	if err := pool.QueryRow(ctx, `SELECT verb FROM chat_mutation_audit WHERE id = $1`, auditID).Scan(&verbCol); err != nil {
		t.Fatalf("audit readback: %v", err)
	}
	if verbCol != "reject_hire" {
		t.Errorf("verb = %q; want reject_hire", verbCol)
	}

	// No agents row landed.
	var agentRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agents`).Scan(&agentRows); err != nil {
		t.Fatalf("agents count: %v", err)
	}
	if agentRows != 0 {
		t.Errorf("agents rows = %d; want 0", agentRows)
	}
}
