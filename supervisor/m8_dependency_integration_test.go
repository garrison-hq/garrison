//go:build integration

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/garrisonmutate"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestM8DependencyCreateUnblockCycle exercises spec US2 in three
// parts:
//
//  1. Seed A in 'in_dev', B in 'todo' with B.depends_on=A. The
//     verb-level dep-gate is exercised in unit tests; here we focus
//     on the create_ticket side rejecting cycle + chain-too-deep
//     attempts via the agent caller path.
//  2. Cycle rejection: A→B (manual SQL), then attempt to create C
//     pointing at A — the walker detects the existing A↔B cycle and
//     rejects the new ticket with dependency_cycle.
//  3. Chain-too-deep: a 33-hop chain rejects with
//     dependency_chain_too_deep.
//
// The block/unblock spawn-prep behavior is exercised at unit-test
// level (TestSpawnPrepBlocks…, TestSpawnPrepUnblocks…); this top-
// level test pins the verb-side rejections + the cross-dept
// dependency invariant.
func TestM8DependencyCreateUnblockCycle(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, agent_instances, tickets, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('dep-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/m8dep')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}

	// Build the parent + agent_instance the agent caller path needs.
	var parentTicketID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'parent', 'in_dev') RETURNING id`, deptID).Scan(&parentTicketID); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	var agentInstanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (department_id, role_slug, ticket_id, status)
		 VALUES ($1, 'engineer', $2, 'running') RETURNING id`, deptID, parentTicketID).Scan(&agentInstanceID); err != nil {
		t.Fatalf("seed agent_instance: %v", err)
	}
	deps := garrisonmutate.Deps{Pool: pool, AgentInstanceID: agentInstanceID}
	verb := garrisonmutate.FindVerb("create_ticket")
	if verb == nil {
		t.Fatal("create_ticket missing from registry")
	}

	// Part 2: cycle. Seed A → B → A (manual close).
	var a pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'A', 'todo') RETURNING id`, deptID).Scan(&a); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	var b pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug, depends_on_ticket_id)
		 VALUES ($1, 'B', 'todo', $2) RETURNING id`, deptID, a).Scan(&b); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE tickets SET depends_on_ticket_id = $2 WHERE id = $1`, a, b); err != nil {
		t.Fatalf("close cycle: %v", err)
	}
	cycleArgs := fmt.Sprintf(
		`{"objective":"C","department_slug":"engineering","depends_on_ticket_id":%q}`, uuidString(a))
	res, err := verb.Handler(ctx, deps, json.RawMessage(cycleArgs))
	if err != nil {
		t.Fatalf("cycle handler: %v", err)
	}
	if res.Success {
		t.Errorf("expected cycle rejection; got %+v", res)
	}
	if res.ErrorKind != "dependency_cycle" {
		t.Errorf("ErrorKind = %q; want dependency_cycle", res.ErrorKind)
	}

	// Part 3: chain-too-deep. Seed 33-hop chain in a fresh dept.
	if _, err := pool.Exec(ctx, `UPDATE tickets SET depends_on_ticket_id = NULL WHERE id = $1`, a); err != nil {
		t.Fatalf("clear cycle: %v", err)
	}
	var prev, head pgtype.UUID
	for i := 0; i < 33; i++ {
		var id pgtype.UUID
		if !prev.Valid {
			if err := pool.QueryRow(ctx,
				`INSERT INTO tickets (department_id, objective, column_slug)
				 VALUES ($1, $2, 'todo') RETURNING id`,
				deptID, fmt.Sprintf("chain-%d", i)).Scan(&id); err != nil {
				t.Fatalf("seed chain head: %v", err)
			}
		} else {
			if err := pool.QueryRow(ctx,
				`INSERT INTO tickets (department_id, objective, column_slug, depends_on_ticket_id)
				 VALUES ($1, $2, 'todo', $3) RETURNING id`,
				deptID, fmt.Sprintf("chain-%d", i), prev).Scan(&id); err != nil {
				t.Fatalf("seed chain[%d]: %v", i, err)
			}
		}
		prev = id
		head = id
	}
	deepArgs := fmt.Sprintf(
		`{"objective":"too_deep","department_slug":"engineering","depends_on_ticket_id":%q}`,
		uuidString(head))
	res, err = verb.Handler(ctx, deps, json.RawMessage(deepArgs))
	if err != nil {
		t.Fatalf("deep handler: %v", err)
	}
	if res.Success {
		t.Errorf("expected deep rejection; got %+v", res)
	}
	if res.ErrorKind != "dependency_chain_too_deep" {
		t.Errorf("ErrorKind = %q; want dependency_chain_too_deep", res.ErrorKind)
	}
}

// uuidString renders a pgtype.UUID in canonical 36-char form for
// embedding into JSON args.
func uuidString(u pgtype.UUID) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
