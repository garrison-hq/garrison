//go:build integration

package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// operatorUUID is the canonical "operator clicked the approve button"
// UUID for the integration tests. Matches the same shape setupIntegration
// uses for chat sessions but lives in approved_by, not chat_sessions.
func operatorUUID() pgtype.UUID {
	return pgtype.UUID{Valid: true, Bytes: [16]byte{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa}}
}

// seedSkillChangeProposal writes a hiring_proposals row of type
// 'skill_change' targeting the fixture's seeded agent. Returns the
// proposal id.
func seedSkillChangeProposal(t *testing.T, fx integrationFixture) pgtype.UUID {
	t.Helper()
	args, _ := json.Marshal(ProposeSkillChangeArgs{
		AgentRoleSlug:   fx.agentRoleSlug,
		JustificationMD: "operator wants this skill",
		Add: []SkillEntry{{
			Package: "skills.sh/sample",
			Version: "v1.0.0",
			Digest:  strings.Repeat("a", 64),
		}},
	})
	r, err := realProposeSkillChangeHandler(context.Background(), fx.deps, args)
	if err != nil || !r.Success {
		t.Fatalf("seed skill_change proposal: %v / %+v", err, r)
	}
	return uuidFromString(t, r.AffectedResourceID)
}

// seedHireProposal walks the existing M5.3 propose_hire path so the
// approve_hire test has a real pending row to flip. Skips skill seeding.
func seedHireProposal(t *testing.T, fx integrationFixture) pgtype.UUID {
	t.Helper()
	args, _ := json.Marshal(ProposeHireArgs{
		RoleTitle:       "growth-strategist",
		DepartmentSlug:  fx.departmentSlug,
		JustificationMD: "we need someone to drive the growth funnel",
		SkillsSummaryMD: "growth, analytics",
	})
	r, err := realProposeHireHandler(context.Background(), fx.deps, args)
	if err != nil || !r.Success {
		t.Fatalf("seed propose_hire: %v / %+v", err, r)
	}
	return uuidFromString(t, r.AffectedResourceID)
}

func uuidFromString(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("parse uuid %q: %v", s, err)
	}
	return u
}

// -------- ApproveHire ---------------------------------------------------

func TestApproveHire_WritesAgentRowAndAudit(t *testing.T) {
	fx := setupIntegration(t)
	proposalID := seedHireProposal(t, fx)

	ctx := context.Background()
	tx, err := fx.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := ApproveHire(ctx, tx, proposalID, operatorUUID())
	if err != nil {
		t.Fatalf("ApproveHire: %v", err)
	}
	if !res.AgentID.Valid {
		t.Fatal("AgentID not set")
	}
	if !res.AuditID.Valid {
		t.Fatal("AuditID not set")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Agent row landed.
	var (
		roleSlug string
		status   string
	)
	if err := fx.pool.QueryRow(ctx,
		`SELECT role_slug, status FROM agents WHERE id = $1`, res.AgentID).
		Scan(&roleSlug, &status); err != nil {
		t.Fatalf("readback agent: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q; want active", status)
	}
	if roleSlug == "" {
		t.Errorf("role_slug empty")
	}

	// Proposal flipped to install_in_progress.
	var pStatus string
	if err := fx.pool.QueryRow(ctx,
		`SELECT status FROM hiring_proposals WHERE id = $1`, proposalID).Scan(&pStatus); err != nil {
		t.Fatalf("readback proposal: %v", err)
	}
	if pStatus != "install_in_progress" {
		t.Errorf("proposal status = %q; want install_in_progress", pStatus)
	}

	// Audit row landed with the right verb.
	var verb string
	if err := fx.pool.QueryRow(ctx,
		`SELECT verb FROM chat_mutation_audit WHERE id = $1`, res.AuditID).Scan(&verb); err != nil {
		t.Fatalf("readback audit: %v", err)
	}
	if verb != "approve_hire" {
		t.Errorf("verb = %q; want approve_hire", verb)
	}
}

func TestApproveHire_RejectsAlreadyApproved(t *testing.T) {
	fx := setupIntegration(t)
	proposalID := seedHireProposal(t, fx)

	ctx := context.Background()
	tx1, _ := fx.pool.Begin(ctx)
	if _, err := ApproveHire(ctx, tx1, proposalID, operatorUUID()); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	tx2, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx2.Rollback(ctx) }()
	if _, err := ApproveHire(ctx, tx2, proposalID, operatorUUID()); !errors.Is(err, ErrProposalNotPending) {
		t.Errorf("second approve err = %v; want ErrProposalNotPending", err)
	}
}

func TestApproveHire_NotFound(t *testing.T) {
	fx := setupIntegration(t)
	bogus := pgtype.UUID{Valid: true, Bytes: [16]byte{0x9}}

	ctx := context.Background()
	tx, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := ApproveHire(ctx, tx, bogus, operatorUUID()); !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("err = %v; want ErrProposalNotFound", err)
	}
}

// -------- ApproveSkillChange + FR-110a supersession ---------------------

func TestApproveSkillChange_SupersedesSiblings(t *testing.T) {
	fx := setupIntegration(t)
	first := seedSkillChangeProposal(t, fx)
	second := seedSkillChangeProposal(t, fx)

	ctx := context.Background()
	tx, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := ApproveSkillChange(ctx, tx, first, operatorUUID())
	if err != nil {
		t.Fatalf("ApproveSkillChange: %v", err)
	}
	if res.SupersededCount != 1 {
		t.Errorf("SupersededCount = %d; want 1", res.SupersededCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Second proposal flipped to superseded with the right reason.
	var (
		status string
		reason *string
	)
	if err := fx.pool.QueryRow(ctx,
		`SELECT status, rejected_reason FROM hiring_proposals WHERE id = $1`, second).
		Scan(&status, &reason); err != nil {
		t.Fatalf("readback sibling: %v", err)
	}
	if status != "superseded" {
		t.Errorf("sibling status = %q; want superseded", status)
	}
	if reason == nil || !strings.HasPrefix(*reason, "superseded_by:") {
		t.Errorf("sibling rejected_reason = %v; want superseded_by:...", reason)
	}
}

func TestApproveSkillChange_RejectsWrongType(t *testing.T) {
	fx := setupIntegration(t)
	hireID := seedHireProposal(t, fx)

	ctx := context.Background()
	tx, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := ApproveSkillChange(ctx, tx, hireID, operatorUUID()); !errors.Is(err, ErrProposalTypeMismatch) {
		t.Errorf("err = %v; want ErrProposalTypeMismatch", err)
	}
}

// -------- ApproveVersionBump --------------------------------------------

func seedVersionBumpProposal(t *testing.T, fx integrationFixture) pgtype.UUID {
	t.Helper()
	args, _ := json.Marshal(BumpSkillVersionArgs{
		AgentRoleSlug: fx.agentRoleSlug,
		Package:       "skills.sh/sample",
		FromVersion:   "v1.0.0",
		ToVersion:     "v1.1.0",
		FromDigest:    strings.Repeat("a", 64),
		ToDigest:      strings.Repeat("b", 64),
	})
	r, err := realBumpSkillVersionHandler(context.Background(), fx.deps, args)
	if err != nil || !r.Success {
		t.Fatalf("seed version_bump: %v / %+v", err, r)
	}
	return uuidFromString(t, r.AffectedResourceID)
}

func TestApproveVersionBump_SupersedesSiblings(t *testing.T) {
	fx := setupIntegration(t)
	first := seedVersionBumpProposal(t, fx)
	second := seedVersionBumpProposal(t, fx)

	ctx := context.Background()
	tx, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := ApproveVersionBump(ctx, tx, first, operatorUUID())
	if err != nil {
		t.Fatalf("ApproveVersionBump: %v", err)
	}
	if res.SupersededCount != 1 {
		t.Errorf("SupersededCount = %d; want 1", res.SupersededCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var status string
	if err := fx.pool.QueryRow(ctx,
		`SELECT status FROM hiring_proposals WHERE id = $1`, second).Scan(&status); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "superseded" {
		t.Errorf("sibling status = %q; want superseded", status)
	}
}

// -------- RejectProposal ------------------------------------------------

func TestRejectProposal_PersistsRowWithReason(t *testing.T) {
	fx := setupIntegration(t)
	proposalID := seedHireProposal(t, fx)

	ctx := context.Background()
	tx, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	auditID, err := RejectProposal(ctx, tx, proposalID, operatorUUID(), "the role is already covered")
	if err != nil {
		t.Fatalf("RejectProposal: %v", err)
	}
	if !auditID.Valid {
		t.Fatal("auditID not set")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var (
		status string
		reason *string
	)
	if err := fx.pool.QueryRow(ctx,
		`SELECT status, rejected_reason FROM hiring_proposals WHERE id = $1`, proposalID).
		Scan(&status, &reason); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "rejected" {
		t.Errorf("status = %q; want rejected", status)
	}
	if reason == nil || !strings.Contains(*reason, "already covered") {
		t.Errorf("reason = %v; want substring 'already covered'", reason)
	}

	var verb string
	if err := fx.pool.QueryRow(ctx,
		`SELECT verb FROM chat_mutation_audit WHERE id = $1`, auditID).Scan(&verb); err != nil {
		t.Fatalf("readback audit: %v", err)
	}
	if verb != "reject_hire" {
		t.Errorf("verb = %q; want reject_hire", verb)
	}
}

// -------- UpdateAgentMD (Server-Action only) ----------------------------

func TestUpdateAgentMD_ServerActionOnly(t *testing.T) {
	fx := setupIntegration(t)

	// Sanity: chat verb registry doesn't surface update_agent_md.
	if FindVerb("update_agent_md") != nil {
		t.Fatal("update_agent_md must NOT be a chat verb (F3 lean)")
	}

	ctx := context.Background()
	tx, _ := fx.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()

	auditID, err := UpdateAgentMD(ctx, tx, fx.agentID, "# new agent.md content", operatorUUID())
	if err != nil {
		t.Fatalf("UpdateAgentMD: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var got string
	if err := fx.pool.QueryRow(ctx,
		`SELECT agent_md FROM agents WHERE id = $1`, fx.agentID).Scan(&got); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got != "# new agent.md content" {
		t.Errorf("agent_md = %q; want updated", got)
	}

	// Audit row carries both prior + new MD content for forensics.
	var argsJSON []byte
	if err := fx.pool.QueryRow(ctx,
		`SELECT args_jsonb FROM chat_mutation_audit WHERE id = $1`, auditID).Scan(&argsJSON); err != nil {
		t.Fatalf("readback audit: %v", err)
	}
	if !strings.Contains(string(argsJSON), "prior_agent_md") {
		t.Errorf("audit args missing prior_agent_md: %s", argsJSON)
	}
	if !strings.Contains(string(argsJSON), "new_agent_md") {
		t.Errorf("audit args missing new_agent_md: %s", argsJSON)
	}
}
