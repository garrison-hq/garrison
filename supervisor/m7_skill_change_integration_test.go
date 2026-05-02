//go:build integration

// M7 T019 (skill_change + FR-110a slice) — propose_skill_change →
// approve flow with sibling supersession.

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

func TestM7SkillChangeApproveSupersedesSiblings(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, hiring_proposals, chat_messages, chat_sessions,
		         agent_install_journal, agent_container_events, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	var companyID, deptID, sessionID, messageID, agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name) VALUES ('m7 sc co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-sc')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (department_id, role_slug, listens_for, agent_md, model, status)
		 VALUES ($1, 'engineering.engineer', '[]'::jsonb, 'agent prose', 'claude-x', 'active')
		 RETURNING id`, deptID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_sessions (started_by_user_id, status, total_cost_usd)
		 VALUES ($1, 'active', 0)
		 RETURNING id`, pgtype.UUID{Valid: true, Bytes: [16]byte{0xee}}).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO chat_messages (session_id, turn_index, role, status, content)
		 VALUES ($1, 0, 'operator', 'completed', 'add a skill')
		 RETURNING id`, sessionID).Scan(&messageID); err != nil {
		t.Fatalf("seed message: %v", err)
	}

	deps := garrisonmutate.Deps{
		Pool:          pool,
		ChatSessionID: sessionID,
		ChatMessageID: messageID,
	}
	digest := strings.Repeat("a", 64)
	mkPropose := func() pgtype.UUID {
		args, _ := json.Marshal(garrisonmutate.ProposeSkillChangeArgs{
			AgentRoleSlug:   "engineering.engineer",
			JustificationMD: "operator wants this skill",
			Add: []garrisonmutate.SkillEntry{
				{Package: "skills.sh/sample", Version: "v1.0.0", Digest: digest},
			},
		})
		v := garrisonmutate.FindVerb("propose_skill_change")
		r, err := v.Handler(ctx, deps, args)
		if err != nil || !r.Success {
			t.Fatalf("propose_skill_change: %v / %+v", err, r)
		}
		var id pgtype.UUID
		if err := id.Scan(r.AffectedResourceID); err != nil {
			t.Fatalf("parse id: %v", err)
		}
		return id
	}
	first := mkPropose()
	second := mkPropose()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	res, err := garrisonmutate.ApproveSkillChange(ctx, tx, first, pgtype.UUID{Valid: true, Bytes: [16]byte{0xff}})
	if err != nil {
		t.Fatalf("ApproveSkillChange: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.SupersededCount != 1 {
		t.Errorf("SupersededCount = %d; want 1", res.SupersededCount)
	}

	q := store.New(pool)
	prop, err := q.GetHiringProposalByID(ctx, second)
	if err != nil {
		t.Fatalf("readback second: %v", err)
	}
	if prop.Status != "superseded" {
		t.Errorf("second proposal status = %q; want superseded", prop.Status)
	}
	if prop.RejectedReason == nil || !strings.Contains(*prop.RejectedReason, "superseded_by:") {
		t.Errorf("rejected_reason missing supersede prefix: %v", prop.RejectedReason)
	}
}
