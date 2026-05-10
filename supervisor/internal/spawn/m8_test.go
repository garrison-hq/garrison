//go:build integration

package spawn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeVaultFetcher returns a deterministic token for any GrantRow.
type fakeVaultFetcher struct {
	token string
	err   error
}

func (f *fakeVaultFetcher) Fetch(_ context.Context, req []vault.GrantRow) (map[string]vault.SecretValue, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]vault.SecretValue, len(req))
	for _, g := range req {
		out[g.EnvVarName] = vault.New([]byte(f.token))
	}
	return out, nil
}

// depFixture seeds two tickets: A (predecessor) and B (dependent, depends_on=A).
type depFixture struct {
	pool    *pgxpool.Pool
	deptID  pgtype.UUID
	ticketA pgtype.UUID
	ticketB pgtype.UUID
	q       *store.Queries
}

func setupDepFixture(t *testing.T, predecessorColumn string, satisfactionCols []byte) depFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('dep-test-co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if satisfactionCols == nil {
		if err := pool.QueryRow(ctx,
			`INSERT INTO departments (company_id, slug, name, workspace_path)
			 VALUES ($1, 'engineering', 'Engineering', '/tmp/dep')
			 RETURNING id`, companyID).Scan(&deptID); err != nil {
			t.Fatalf("seed dept: %v", err)
		}
	} else {
		if err := pool.QueryRow(ctx,
			`INSERT INTO departments (company_id, slug, name, workspace_path, dependency_satisfaction_columns)
			 VALUES ($1, 'engineering', 'Engineering', '/tmp/dep', $2::jsonb)
			 RETURNING id`, companyID, satisfactionCols).Scan(&deptID); err != nil {
			t.Fatalf("seed dept w/ cols: %v", err)
		}
	}
	var a pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'A', $2) RETURNING id`, deptID, predecessorColumn).Scan(&a); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	var b pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug, depends_on_ticket_id)
		 VALUES ($1, 'B', 'todo', $2) RETURNING id`, deptID, a).Scan(&b); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	return depFixture{
		pool:    pool,
		deptID:  deptID,
		ticketA: a,
		ticketB: b,
		q:       store.New(pool),
	}
}

func TestSpawnPrepBlocksOnUnsatisfiedDependency(t *testing.T) {
	// Predecessor in 'in_dev'; defaults require 'qa_review' or 'done'.
	fx := setupDepFixture(t, "in_dev", nil)
	ok, err := checkDependencySatisfied(context.Background(), fx.q, fx.ticketB)
	if err != nil {
		t.Fatalf("checkDependencySatisfied: %v", err)
	}
	if ok {
		t.Errorf("expected blocked; got allowed")
	}
}

func TestSpawnPrepUnblocksAfterPredecessorTransition(t *testing.T) {
	fx := setupDepFixture(t, "in_dev", nil)
	// Transition A to qa_review.
	if _, err := fx.pool.Exec(context.Background(),
		`UPDATE tickets SET column_slug = 'qa_review' WHERE id = $1`, fx.ticketA); err != nil {
		t.Fatalf("transition A: %v", err)
	}
	ok, err := checkDependencySatisfied(context.Background(), fx.q, fx.ticketB)
	if err != nil {
		t.Fatalf("checkDependencySatisfied: %v", err)
	}
	if !ok {
		t.Errorf("expected unblocked after predecessor transitioned to qa_review")
	}
}

func TestSpawnPrepDependencyNullSkipsCheck(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('co') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/null')
		 RETURNING id`, companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	var solo pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tickets (department_id, objective, column_slug)
		 VALUES ($1, 'solo', 'todo') RETURNING id`, deptID).Scan(&solo); err != nil {
		t.Fatalf("seed solo: %v", err)
	}
	q := store.New(pool)
	ok, err := checkDependencySatisfied(ctx, q, solo)
	if err != nil {
		t.Fatalf("checkDependencySatisfied: %v", err)
	}
	if !ok {
		t.Errorf("NULL depends_on must skip the gate; got blocked")
	}
}

func TestSpawnPrepHonoursPerDeptSatisfactionColumns(t *testing.T) {
	// Operator-configured satisfaction = ["done"] only. Predecessor in
	// 'qa_review' must NOT satisfy this stricter setting.
	fx := setupDepFixture(t, "qa_review", []byte(`["done"]`))
	ok, err := checkDependencySatisfied(context.Background(), fx.q, fx.ticketB)
	if err != nil {
		t.Fatalf("checkDependencySatisfied: %v", err)
	}
	if ok {
		t.Errorf("strict satisfaction = [\"done\"] must block predecessor in qa_review; got allowed")
	}
	// Now transition A to 'done'; the stricter rule unblocks.
	if _, err := fx.pool.Exec(context.Background(),
		`UPDATE tickets SET column_slug = 'done' WHERE id = $1`, fx.ticketA); err != nil {
		t.Fatalf("transition A to done: %v", err)
	}
	ok, err = checkDependencySatisfied(context.Background(), fx.q, fx.ticketB)
	if err != nil {
		t.Fatalf("checkDependencySatisfied second: %v", err)
	}
	if !ok {
		t.Errorf("predecessor in 'done' satisfies strict ['done'] config; got blocked")
	}
}

// agentTokenFixture seeds a company + dept + agent + agent-scoped
// vault grant binding MCPJUNGLE_BEARER_TOKEN to a vault path so
// EnsureMcpjungleTokenForAgent has something to resolve.
type agentTokenFixture struct {
	pool       *pgxpool.Pool
	queries    *store.Queries
	customerID pgtype.UUID
	agentID    pgtype.UUID
}

func seedAgentToken(t *testing.T) agentTokenFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('tok-test') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO departments (company_id, slug, name, workspace_path)
		 VALUES ($1, 'engineering', 'Engineering', '/tmp/tok') RETURNING id`,
		companyID).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	var agentID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for, status)
		 VALUES ($1, 'engineer', '# x', 'claude-h', '[]'::jsonb, '[]'::jsonb, '["x"]'::jsonb, 'active')
		 RETURNING id`, deptID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agent_role_secrets (role_slug, agent_id, secret_path, env_var_name, customer_id, granted_by)
		 VALUES ('engineer', $1, 'mcpjungle/agents/X', $2, $3, 'test')`,
		agentID, vault.MCPJungleBearerTokenEnvVar, companyID,
	); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	return agentTokenFixture{
		pool: pool, queries: store.New(pool), customerID: companyID, agentID: agentID,
	}
}

func TestEnsureMcpjungleTokenFetchesFromVault(t *testing.T) {
	fx := seedAgentToken(t)
	deps := Deps{
		Pool:       fx.pool,
		Queries:    fx.queries,
		Vault:      &fakeVaultFetcher{token: "agent-bearer-1234"},
		CustomerID: fx.customerID,
	}
	env, err := EnsureMcpjungleTokenForAgent(context.Background(), deps, fx.agentID, nil)
	if err != nil {
		t.Fatalf("EnsureMcpjungleTokenForAgent: %v", err)
	}
	if !strings.HasPrefix(env, vault.MCPJungleBearerTokenEnvVar+"=") {
		t.Errorf("env-var assignment shape wrong: %q", env)
	}
	if !strings.Contains(env, "agent-bearer-1234") {
		t.Errorf("token missing from env-var: %q", env)
	}
}

func TestEnsureMcpjungleTokenNoGrantSurfacesNotFound(t *testing.T) {
	fx := seedAgentToken(t)
	// Wipe the grant we just seeded to simulate a pre-reconciler state.
	if _, err := fx.pool.Exec(context.Background(),
		`DELETE FROM agent_role_secrets WHERE agent_id = $1`, fx.agentID); err != nil {
		t.Fatalf("wipe grant: %v", err)
	}
	deps := Deps{
		Pool: fx.pool, Queries: fx.queries,
		Vault:      &fakeVaultFetcher{token: "ignored"},
		CustomerID: fx.customerID,
	}
	_, err := EnsureMcpjungleTokenForAgent(context.Background(), deps, fx.agentID, nil)
	if !errors.Is(err, vault.ErrVaultSecretNotFound) {
		t.Errorf("err = %v; want ErrVaultSecretNotFound", err)
	}
}

func TestEnsureMcpjungleTokenSurfacesUnreachable(t *testing.T) {
	fx := seedAgentToken(t)
	deps := Deps{
		Pool: fx.pool, Queries: fx.queries,
		Vault:      &fakeVaultFetcher{err: vault.ErrVaultUnavailable},
		CustomerID: fx.customerID,
	}
	_, err := EnsureMcpjungleTokenForAgent(context.Background(), deps, fx.agentID, nil)
	if !errors.Is(err, vault.ErrVaultUnavailable) {
		t.Errorf("err = %v; want ErrVaultUnavailable", err)
	}
}
