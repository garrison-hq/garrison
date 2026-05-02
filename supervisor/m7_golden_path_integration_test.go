//go:build integration

// M7 T017 / US1 — golden-path integration test for the hiring flow.
// Exercises: chat-CEO calls propose_hire → row lands in
// hiring_proposals → operator-side Server-Action approve → agents row
// inserted + chat_mutation_audit row written + proposal transitions
// to install_in_progress. The post-approve install pipeline (Actuator)
// is exercised in T019; this test pins the propose → approve data
// shape end-to-end against a real Postgres testcontainer.

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

func TestM7GoldenPathHireProposeAndApprove(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, hiring_proposals, chat_messages, chat_sessions, agent_install_journal,
		         agent_container_events, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	// Seed: company + engineering department + chat session/message.
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('m7 golden path') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-eng')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	operatorID := pgtype.UUID{Valid: true, Bytes: [16]byte{0x01, 0x02, 0x03}}
	var sessionID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd)
		 VALUES ($1, 'active', 0)
		 RETURNING id`, operatorID).Scan(&sessionID); err != nil {
		t.Fatalf("seed chat session: %v", err)
	}
	var messageID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status, content)
		 VALUES ($1, 0, 'operator', 'completed', 'hire growth strategist')
		 RETURNING id`, sessionID).Scan(&messageID); err != nil {
		t.Fatalf("seed chat message: %v", err)
	}

	deps := garrisonmutate.Deps{
		Pool:          pool,
		ChatSessionID: sessionID,
		ChatMessageID: messageID,
	}
	args, _ := json.Marshal(garrisonmutate.ProposeHireArgs{
		RoleTitle:       "growth-strategist",
		DepartmentSlug:  "engineering",
		JustificationMD: "we need someone to drive the growth funnel",
		SkillsSummaryMD: "growth, analytics, brand",
	})

	verb := garrisonmutate.FindVerb("propose_hire")
	if verb == nil {
		t.Fatal("propose_hire verb missing from registry")
	}
	res, err := verb.Handler(ctx, deps, args)
	if err != nil || !res.Success {
		t.Fatalf("propose_hire: %v / %+v", err, res)
	}
	proposalID := pgtype.UUID{}
	if err := proposalID.Scan(res.AffectedResourceID); err != nil {
		t.Fatalf("parse proposal id: %v", err)
	}

	// Approve via the Server-Action helper, mirroring the dashboard's
	// transaction lifecycle.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	approveRes, err := garrisonmutate.ApproveHire(ctx, tx, proposalID, operatorID)
	if err != nil {
		t.Fatalf("ApproveHire: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if !approveRes.AgentID.Valid {
		t.Fatal("AgentID not set")
	}

	// Assertions:
	// 1. agents row exists with status='active'.
	q := store.New(pool)
	agent, err := q.GetAgentByID(ctx, approveRes.AgentID)
	if err != nil {
		t.Fatalf("readback agent: %v", err)
	}
	if agent.Status != "active" {
		t.Errorf("agent.status = %q; want active", agent.Status)
	}
	if !strings.Contains(agent.AgentMd, "growth-strategist") {
		t.Errorf("agent_md missing role title; got %q", agent.AgentMd)
	}

	// 2. proposal flipped to install_in_progress.
	prop, err := q.GetHiringProposalByID(ctx, proposalID)
	if err != nil {
		t.Fatalf("readback proposal: %v", err)
	}
	if prop.Status != "install_in_progress" {
		t.Errorf("proposal.status = %q; want install_in_progress", prop.Status)
	}

	// 3. audit row landed under verb='approve_hire'.
	var verbCol string
	if err := pool.QueryRow(ctx,
		`SELECT verb FROM chat_mutation_audit WHERE id = $1`, approveRes.AuditID).Scan(&verbCol); err != nil {
		t.Fatalf("readback audit: %v", err)
	}
	if verbCol != "approve_hire" {
		t.Errorf("audit verb = %q; want approve_hire", verbCol)
	}
}
