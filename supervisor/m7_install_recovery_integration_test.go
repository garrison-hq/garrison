//go:build integration

// M7 T019 (FR-214a slice) — install recovery: each of the 6 install
// steps lands an agent_install_journal row; an interrupted install
// surfaces the latest journaled step so a restart can resume.
//
// The skillinstall.Actuator full pipeline is exercised by its own
// package's actuator_integration_test.go (T007); this supervisor-level
// test pins the recovery seam by writing journal rows directly + then
// querying the latest-step view skillinstall.recover reads.

package supervisor_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM7InstallJournalSurfacesLatestStepForRecovery(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, hiring_proposals, chat_messages, chat_sessions,
		         agent_install_journal, agent_container_events, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID, deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name) VALUES ('m7 install co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-install')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}

	// Seed a hiring_proposals row in 'install_in_progress' status so
	// the journal FK to proposal_id is satisfied.
	var proposalID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO hiring_proposals (
		    role_title, department_slug, justification_md,
		    proposed_via, proposal_type, status,
		    proposal_snapshot_jsonb
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		"engineer", "engineering", "test", "ceo_chat", "new_agent", "install_in_progress",
		json.RawMessage(`{}`)).Scan(&proposalID); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	q := store.New(pool)
	steps := []string{"download", "verify_digest", "extract", "mount", "container_create"}
	for _, step := range steps {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agent_install_journal (proposal_id, step, outcome, payload_jsonb)
			 VALUES ($1, $2, 'success', '{}'::jsonb)`,
			proposalID, step); err != nil {
			t.Fatalf("seed journal step %q: %v", step, err)
		}
	}

	// Recovery query returns rows by status='install_in_progress'.
	rows, err := q.ListInstallInProgressProposals(ctx)
	if err != nil {
		t.Fatalf("ListInstallInProgressProposals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("install_in_progress rows = %d; want 1", len(rows))
	}

	// Latest journal step is the 5th seeded (container_create); the
	// recovery algorithm resumes by querying for it.
	var latest string
	if err := pool.QueryRow(ctx,
		`SELECT step FROM agent_install_journal
		  WHERE proposal_id = $1
		  ORDER BY created_at DESC LIMIT 1`, proposalID).Scan(&latest); err != nil {
		t.Fatalf("latest step: %v", err)
	}
	if latest != "container_create" {
		t.Errorf("latest step = %q; want container_create", latest)
	}
}
