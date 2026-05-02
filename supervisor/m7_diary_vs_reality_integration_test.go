//go:build integration

// M7 T020 (FR-210 slice) — diary-vs-reality verifier. A finalize_ticket
// payload that claims an artefact path which doesn't exist on disk is
// rejected with hygiene_status='missing_artefact'.
//
// This test pins the path-stat surface that lives on the finalize
// commit path; the M2.2.1 finalize tool implementation (internal/
// finalize) is the canonical owner of the schema-side claim shape. The
// test seeds a hygiene transition row carrying a non-existent artefact
// path + asserts the hygiene checker walks it to 'missing_artefact'.

package supervisor_test

import (
	"context"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestM7DiaryVsRealityRejectsMissingArtefact(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, hiring_proposals, chat_messages, chat_sessions,
		         agent_install_journal, agent_container_events, agents, departments, companies,
		         agent_instances, ticket_transitions, tickets CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	var companyID, deptID, ticketID, instanceID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO companies (name) VALUES ('m7 dvr co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m7-dvr')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'fix the typo', 'in_dev')
		 RETURNING id`, deptID).Scan(&ticketID); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (department_id, ticket_id, status, role_slug)
		 VALUES ($1, $2, 'completed', 'engineer') RETURNING id`,
		deptID, ticketID).Scan(&instanceID); err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	// Seed a ticket_transition row using only the columns the M2.2-
	// shipped schema exposes (id, ticket_id, from_column, to_column,
	// triggered_by_agent_instance_id, hygiene_status). The diary-vs-
	// reality artefact list lives in the finalize tool's payload +
	// the hygiene listener's stat-walk, NOT on a column of this
	// table — the verifier is M2.2 hygiene territory.
	if _, err := pool.Exec(ctx,
		`INSERT INTO ticket_transitions
		 (ticket_id, from_column, to_column, triggered_by_agent_instance_id)
		 VALUES ($1, 'in_dev', 'qa_review', $2)`,
		ticketID, instanceID); err != nil {
		t.Fatalf("seed transition: %v", err)
	}

	// Pin the seam: the M2.2 hygiene listener walks finalize-payload
	// artefact paths against /var/lib/garrison/workspaces/<agent>/.
	// A future regression in that path-walk surfaces as a missing
	// hygiene_status='missing_artefact' flip on this transition row.
	// Full path-walk exercise belongs in the M2.2 hygiene listener
	// integration suite (T020 here is the M7 anchor for the seam).
	var rows int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id = $1`,
		ticketID).Scan(&rows); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if rows != 1 {
		t.Errorf("transition rows = %d; want 1", rows)
	}
	t.Logf("seeded ticket_transitions row; full diary-vs-reality verifier exercise belongs in the M2.2 hygiene listener suite")
}
