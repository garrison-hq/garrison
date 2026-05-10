//go:build integration

package mcpserverwork_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpjungle"
	"github.com/garrison-hq/garrison/supervisor/internal/mcpserverwork"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeVault returns a static token for any path; tracks fetches.
type fakeVault struct {
	mu    sync.Mutex
	token string
	fail  error
	paths []string
}

func (f *fakeVault) FetchOne(_ context.Context, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	if f.fail != nil {
		return "", f.fail
	}
	return f.token, nil
}

type workerFixture struct {
	pool    *pgxpool.Pool
	queries *store.Queries
	worker  *mcpserverwork.Worker
	server  *httptest.Server
	vault   *fakeVault
	// counters set by the stub
	registerHits int
	registerMu   sync.Mutex
}

func seedWorker(t *testing.T, opts struct {
	StubStatus int    // 201/409/500 — controls MCPJungle response
	StubBody   string // body returned for 201; ignored otherwise
	BearerPath *string
	VaultToken string
	VaultErr   error
}) *workerFixture {
	t.Helper()
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE chat_mutation_audit, mcp_servers, agent_instances, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO companies (name, customer_slug) VALUES ('worker-co', 'garrison')`); err != nil {
		// Customer slug 'garrison' may already exist from migration seed;
		// fall back to plain INSERT against the seeded company.
		var existing int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM companies WHERE customer_slug = 'garrison'`).Scan(&existing)
		if existing == 0 {
			t.Fatalf("seed company: %v", err)
		}
	}
	fx := &workerFixture{
		pool:    pool,
		queries: store.New(pool),
		vault:   &fakeVault{token: opts.VaultToken, fail: opts.VaultErr},
	}
	fx.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/servers" {
			fx.registerMu.Lock()
			fx.registerHits++
			fx.registerMu.Unlock()
			w.WriteHeader(opts.StubStatus)
			if opts.StubStatus == http.StatusCreated {
				body := opts.StubBody
				if body == "" {
					body = `{"id":"server-1"}`
				}
				_, _ = w.Write([]byte(body))
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(fx.server.Close)
	client := mcpjungle.NewClient(fx.server.URL, "admin-tok", nil)
	worker, err := mcpserverwork.New(mcpserverwork.Deps{
		Queries: fx.queries,
		Client:  client,
		Vault:   fx.vault,
	})
	if err != nil {
		t.Fatalf("mcpserverwork.New: %v", err)
	}
	fx.worker = worker
	return fx
}

func insertPendingRow(t *testing.T, q *store.Queries, name string, bearer *string) pgtype.UUID {
	t.Helper()
	url := "http://upstream.example/mcp"
	row, err := q.InsertMcpServer(context.Background(), store.InsertMcpServerParams{
		CustomerSlug:    "garrison",
		Name:            name,
		Transport:       "http",
		Url:             &url,
		BearerTokenPath: bearer,
		RegisteredBy:    pgtype.UUID{},
	})
	if err != nil {
		t.Fatalf("InsertMcpServer: %v", err)
	}
	return row.ID
}

func TestWorkerPicksUpRegistrationEvent(t *testing.T) {
	fx := seedWorker(t, struct {
		StubStatus int
		StubBody   string
		BearerPath *string
		VaultToken string
		VaultErr   error
	}{StubStatus: http.StatusCreated})
	id := insertPendingRow(t, fx.queries, "garrison.linear", nil)

	if err := fx.worker.Handle(context.Background(), id); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if fx.registerHits != 1 {
		t.Errorf("MCPJungle hit %d times; want 1", fx.registerHits)
	}
	row, err := fx.queries.GetMcpServerByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMcpServerByID: %v", err)
	}
	if row.Status != "registered" {
		t.Errorf("status = %s; want registered", row.Status)
	}
	if !row.RegisteredAt.Valid {
		t.Errorf("registered_at not set")
	}
	pool := fx.pool
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_mutation_audit
		  WHERE verb = 'register_mcp_server'
		    AND outcome = 'success'`,
	).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 1 {
		t.Errorf("audit rows = %d; want 1", count)
	}
	_ = id
}

func TestWorkerFailedRegistrationWritesFailureRow(t *testing.T) {
	fx := seedWorker(t, struct {
		StubStatus int
		StubBody   string
		BearerPath *string
		VaultToken string
		VaultErr   error
	}{StubStatus: http.StatusInternalServerError})
	id := insertPendingRow(t, fx.queries, "garrison.broken", nil)

	if err := fx.worker.Handle(context.Background(), id); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	row, err := fx.queries.GetMcpServerByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMcpServerByID: %v", err)
	}
	if row.Status != "failed" {
		t.Errorf("status = %s; want failed", row.Status)
	}
	if row.FailureReason == nil || !strings.Contains(*row.FailureReason, "MCPJungle") {
		t.Errorf("failure_reason = %v; want MCPJungle context", row.FailureReason)
	}
	pool := fx.pool
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM chat_mutation_audit
		  WHERE verb = 'register_mcp_server' AND outcome = 'failed'`,
	).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 1 {
		t.Errorf("audit failed rows = %d; want 1", count)
	}
}

func TestWorkerHonoursCtxCancel(t *testing.T) {
	fx := seedWorker(t, struct {
		StubStatus int
		StubBody   string
		BearerPath *string
		VaultToken string
		VaultErr   error
	}{StubStatus: http.StatusCreated})
	id := insertPendingRow(t, fx.queries, "garrison.late", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancel
	err := fx.worker.Handle(ctx, id)
	if err == nil {
		t.Errorf("expected error when ctx already cancelled; got nil")
	}
}

func TestWorkerNoRetryOnFailure(t *testing.T) {
	fx := seedWorker(t, struct {
		StubStatus int
		StubBody   string
		BearerPath *string
		VaultToken string
		VaultErr   error
	}{StubStatus: http.StatusInternalServerError})
	id := insertPendingRow(t, fx.queries, "garrison.once", nil)
	if err := fx.worker.Handle(context.Background(), id); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	// Second call: row is now in 'failed' state; worker must short-circuit
	// rather than re-call MCPJungle.
	prevHits := fx.registerHits
	if err := fx.worker.Handle(context.Background(), id); err != nil {
		t.Fatalf("second Handle: %v", err)
	}
	if fx.registerHits != prevHits {
		t.Errorf("worker retried (hits went from %d to %d); want no retry on terminal state",
			prevHits, fx.registerHits)
	}
}

func TestWorkerFetchesBearerTokenFromVaultIfPathSet(t *testing.T) {
	path := "mcp_servers/garrison.linear/bearer"
	fx := seedWorker(t, struct {
		StubStatus int
		StubBody   string
		BearerPath *string
		VaultToken string
		VaultErr   error
	}{StubStatus: http.StatusCreated, VaultToken: "upstream-bearer-xyz"})
	id := insertPendingRow(t, fx.queries, "garrison.linear", &path)
	if err := fx.worker.Handle(context.Background(), id); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(fx.vault.paths) != 1 || fx.vault.paths[0] != path {
		t.Errorf("vault.paths = %v; want [%q]", fx.vault.paths, path)
	}
}

func TestWorkerVaultFailureFlipsToFailed(t *testing.T) {
	path := "mcp_servers/garrison.broken/bearer"
	fx := seedWorker(t, struct {
		StubStatus int
		StubBody   string
		BearerPath *string
		VaultToken string
		VaultErr   error
	}{StubStatus: http.StatusCreated, VaultErr: errors.New("vault: gone")})
	id := insertPendingRow(t, fx.queries, "garrison.broken", &path)
	if err := fx.worker.Handle(context.Background(), id); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	row, err := fx.queries.GetMcpServerByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetMcpServerByID: %v", err)
	}
	if row.Status != "failed" {
		t.Errorf("status = %s; want failed", row.Status)
	}
	if fx.registerHits != 0 {
		t.Errorf("MCPJungle was called %d times despite vault failure", fx.registerHits)
	}
}
