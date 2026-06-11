//go:build integration

package mcpjungle_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpjungle"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeVault records every WriteSecret invocation so reconcile_test can
// assert on path + value shape without standing up a live Infisical.
type fakeVault struct {
	mu      sync.Mutex
	writes  map[string]string // path -> value
	failNth int               // 0 = never fail; N>0 means the Nth call returns errInjected
	calls   int
}

func newFakeVault() *fakeVault {
	return &fakeVault{writes: map[string]string{}}
}

var errInjected = errors.New("fakeVault: injected error")

func (f *fakeVault) WriteSecret(_ context.Context, path, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failNth > 0 && f.calls == f.failNth {
		return errInjected
	}
	f.writes[path] = value
	return nil
}

// reconcileFixture wires the moving pieces for a reconciler integration
// test: a fresh testdb pool, a single company + department + active
// agent, a stub MCPJungle server, and a fake vault.
type reconcileFixture struct {
	pool      *pgxpool.Pool
	queries   *store.Queries
	companyID pgtype.UUID
	agentID   pgtype.UUID
	roleSlug  string
	server    *httptest.Server
	createHit int
	deleteHit int
	mcpClient *mcpjungle.Client
	vault     *fakeVault
}

func seedReconcile(t *testing.T, allowList []string) *reconcileFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE agent_role_secrets, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	fx := &reconcileFixture{
		pool:     pool,
		queries:  store.New(pool),
		roleSlug: "engineer",
		vault:    newFakeVault(),
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (name) VALUES ('reconcile-co') RETURNING id`,
	).Scan(&fx.companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var deptID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (company_id, slug, name, workspace_path)
		VALUES ($1, 'engineering', 'Engineering', '/tmp/reconcile')
		RETURNING id`, fx.companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("seed dept: %v", err)
	}
	mcpServers := "[]"
	if len(allowList) > 0 {
		b, _ := json.Marshal(allowList)
		mcpServers = string(b)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (department_id, role_slug, agent_md, model, skills, mcp_tools, listens_for,
		                    palace_wing, status, mcp_servers_jsonb)
		VALUES ($1, 'engineer', '# Engineer', 'claude-haiku-4-5-20251001',
		        '[]'::jsonb, '[]'::jsonb, '["x"]'::jsonb, NULL, 'active', $2::jsonb)
		RETURNING id`, deptID, mcpServers,
	).Scan(&fx.agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// Stub MCPJungle: 201 on the first CreateMcpClient, 409 on repeats.
	createdNames := map[string]bool{}
	var mu sync.Mutex
	fx.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/clients":
			mu.Lock()
			defer mu.Unlock()
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if createdNames[body.Name] {
				w.WriteHeader(http.StatusConflict)
				return
			}
			createdNames[body.Name] = true
			fx.createHit++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "client-x", "name": body.Name})
		case r.Method == http.MethodDelete && len(r.URL.Path) > len("/api/v0/clients/"):
			fx.deleteHit++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fx.server.Close)
	fx.mcpClient = mcpjungle.NewClient(fx.server.URL, "admin-tok", nil)
	return fx
}

func TestReconcileCreatesForMissingAgent(t *testing.T) {
	fx := seedReconcile(t, []string{"garrison.linear"})
	report, err := mcpjungle.ReconcileMcpClients(context.Background(), mcpjungle.ReconcileDeps{
		Client:       fx.mcpClient,
		Pool:         fx.pool,
		Queries:      fx.queries,
		VaultClient:  fx.vault,
		CustomerID:   fx.companyID,
		CustomerSlug: "garrison",
	})
	if err != nil {
		t.Fatalf("ReconcileMcpClients: %v", err)
	}
	if len(report.Created) != 1 {
		t.Errorf("Created = %d; want 1", len(report.Created))
	}
	if len(report.Existing) != 0 {
		t.Errorf("Existing = %d; want 0", len(report.Existing))
	}
	if len(report.Failed) != 0 {
		t.Errorf("Failed = %+v; want none", report.Failed)
	}
	if fx.createHit != 1 {
		t.Errorf("MCPJungle CreateMcpClient called %d times; want 1", fx.createHit)
	}
	// Vault write happened at mcpjungle/agents/<agent-id>.
	if len(fx.vault.writes) != 1 {
		t.Fatalf("vault writes = %d; want 1", len(fx.vault.writes))
	}
	for path, val := range fx.vault.writes {
		if path[:len("mcpjungle/agents/")] != "mcpjungle/agents/" {
			t.Errorf("vault path = %q; want mcpjungle/agents/ prefix", path)
		}
		if len(val) != 64 {
			t.Errorf("token len = %d; want 64 hex chars", len(val))
		}
	}
	// Grant row landed in agent_role_secrets.
	var count int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_role_secrets WHERE agent_id = $1 AND env_var_name = $2`,
		fx.agentID, vault.MCPJungleBearerTokenEnvVar,
	).Scan(&count); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if count != 1 {
		t.Errorf("grant rows = %d; want 1", count)
	}
}

func TestReconcileIsIdempotent(t *testing.T) {
	fx := seedReconcile(t, nil)
	// First pass creates.
	_, err := mcpjungle.ReconcileMcpClients(context.Background(), mcpjungle.ReconcileDeps{
		Client:       fx.mcpClient,
		Pool:         fx.pool,
		Queries:      fx.queries,
		VaultClient:  fx.vault,
		CustomerID:   fx.companyID,
		CustomerSlug: "garrison",
	})
	if err != nil {
		t.Fatalf("first ReconcileMcpClients: %v", err)
	}
	// Second pass: MCPJungle returns 409 -> outcomeExisting -> no
	// vault write + no second grant row.
	prevVaultCalls := fx.vault.calls
	report, err := mcpjungle.ReconcileMcpClients(context.Background(), mcpjungle.ReconcileDeps{
		Client:       fx.mcpClient,
		Pool:         fx.pool,
		Queries:      fx.queries,
		VaultClient:  fx.vault,
		CustomerID:   fx.companyID,
		CustomerSlug: "garrison",
	})
	if err != nil {
		t.Fatalf("second ReconcileMcpClients: %v", err)
	}
	if len(report.Created) != 0 {
		t.Errorf("Created on second pass = %d; want 0", len(report.Created))
	}
	if len(report.Existing) != 1 {
		t.Errorf("Existing on second pass = %d; want 1", len(report.Existing))
	}
	if fx.vault.calls != prevVaultCalls {
		t.Errorf("vault.calls grew from %d to %d on idempotent pass; want unchanged",
			prevVaultCalls, fx.vault.calls)
	}
}

func TestReconcileFailsAndRollsBackOnVaultError(t *testing.T) {
	fx := seedReconcile(t, nil)
	fx.vault.failNth = 1 // first WriteSecret fails
	report, err := mcpjungle.ReconcileMcpClients(context.Background(), mcpjungle.ReconcileDeps{
		Client:       fx.mcpClient,
		Pool:         fx.pool,
		Queries:      fx.queries,
		VaultClient:  fx.vault,
		CustomerID:   fx.companyID,
		CustomerSlug: "garrison",
	})
	if err != nil {
		t.Fatalf("ReconcileMcpClients: %v", err)
	}
	if len(report.Failed) != 1 {
		t.Fatalf("Failed = %d; want 1", len(report.Failed))
	}
	if fx.deleteHit != 1 {
		t.Errorf("rollback DeleteMcpClient called %d times; want 1", fx.deleteHit)
	}
	// No grant row should have landed.
	var count int
	if err := fx.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_role_secrets WHERE agent_id = $1`,
		fx.agentID,
	).Scan(&count); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if count != 0 {
		t.Errorf("grant rows after rollback = %d; want 0", count)
	}
}
