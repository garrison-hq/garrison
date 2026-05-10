//go:build chaos

// M8 T020 — chaos test extensions for the runtime additions in M8.
// Each test exercises a failure mode that the M8 supervisor must
// survive without losing rows, stranding containers, or violating
// FR-306's single-audit-row invariant.

package supervisor_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpjungle"
	"github.com/garrison-hq/garrison/supervisor/internal/mcpserverwork"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
)

// TestM8MCPJungleUnreachableAtStartup — supervisor must boot the rest
// of the system when MCPJungle is connection-refused. We can't boot
// the actual supervisor binary here (the chaos suite has the runtime
// preconditions), but we exercise the same shape: when the worker's
// upstream is unreachable, Handle returns an error and the row's
// status flips to 'failed' (no panic, no orphan).
func TestM8MCPJungleUnreachableAtStartup(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE mcp_servers, chat_mutation_audit, agent_instances, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO companies (name, customer_slug) VALUES ('chaos-co', 'garrison')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	// Stub server we close immediately so the worker's HTTP calls all
	// fail with connection-refused.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.Close()
	client := mcpjungle.NewClient(srv.URL, "tok", nil)
	q := store.New(pool)
	worker, err := mcpserverwork.New(mcpserverwork.Deps{Queries: q, Client: client})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	url := "http://upstream-mcp:9000"
	row, err := q.InsertMcpServer(ctx, store.InsertMcpServerParams{
		CustomerSlug: "garrison",
		Name:         "garrison.unreachable",
		Transport:    "http",
		Url:          &url,
	})
	if err != nil {
		t.Fatalf("InsertMcpServer: %v", err)
	}
	// Worker survives the unreachable upstream + records the failure.
	if err := worker.Handle(ctx, row.ID); err != nil {
		t.Fatalf("worker.Handle returned error (expected nil + 'failed' row): %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM mcp_servers WHERE id = $1`, row.ID).Scan(&status); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "failed" {
		t.Errorf("status = %s; want failed (degrade-with-warning posture FR-308)", status)
	}
}

// TestM8RegistrationRequestIdempotentEndState — concurrent dispatch
// (LISTEN+poll double-fire from the M1 dispatcher pattern) must
// converge to a single 'registered' row + at most one 'success' audit
// row regardless of how many goroutines pick up the same mcp_servers
// id. M8's worker-level dedupe is best-effort (status check on read);
// the dispatcher's in-flight sync.Map is the primary guard. This test
// pins the end-state invariant rather than the call-count invariant
// because the dispatcher isn't in the loop here.
func TestM8RegistrationRequestIdempotentEndState(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`TRUNCATE mcp_servers, chat_mutation_audit, agent_instances, agents, departments, companies CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO companies (name, customer_slug) VALUES ('dedupe-co', 'garrison')`); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var registerHits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/servers" {
			atomic.AddInt64(&registerHits, 1)
			// Simulate a slow MCPJungle so the second goroutine's worker
			// has time to observe the first goroutine's status flip.
			time.Sleep(50 * time.Millisecond)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"srv-dedupe"}`))
	}))
	defer srv.Close()
	client := mcpjungle.NewClient(srv.URL, "tok", nil)
	q := store.New(pool)
	worker, err := mcpserverwork.New(mcpserverwork.Deps{Queries: q, Client: client, Pool: pool})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	url := "http://upstream:9000"
	row, err := q.InsertMcpServer(ctx, store.InsertMcpServerParams{
		CustomerSlug: "garrison", Name: "garrison.dedupe", Transport: "http", Url: &url,
	})
	if err != nil {
		t.Fatalf("InsertMcpServer: %v", err)
	}
	// Two concurrent Handle calls — second must short-circuit because
	// the first has flipped the row out of 'pending'.
	doneA := make(chan error, 1)
	doneB := make(chan error, 1)
	go func() { doneA <- worker.Handle(ctx, row.ID) }()
	time.Sleep(10 * time.Millisecond)
	go func() { doneB <- worker.Handle(ctx, row.ID) }()
	if err := <-doneA; err != nil {
		t.Fatalf("worker A: %v", err)
	}
	if err := <-doneB; err != nil {
		t.Fatalf("worker B: %v", err)
	}
	// End-state invariant: row is 'registered' and exactly one
	// 'success' audit row landed. Multiple POST /servers is acceptable
	// here because MCPJungle's own UNIQUE on (customer_slug, name)
	// returns 409 on the duplicate; the worker's status guard
	// short-circuits the second UPDATE.
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM mcp_servers WHERE id = $1`, row.ID).Scan(&status); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if status != "registered" {
		t.Errorf("end-state status = %s; want registered", status)
	}
	var successCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM chat_mutation_audit
		 WHERE verb = 'register_mcp_server' AND outcome = 'success'`).Scan(&successCount); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	// At most one success row from this single mcp_servers id.
	if successCount > 1 {
		t.Errorf("success audit rows = %d; want ≤1 (FR-306 single-row invariant)", successCount)
	}
	// Defend against test silently regressing: we expect SOME hits
	// happened so the test is actually exercising the path.
	if atomic.LoadInt64(&registerHits) == 0 {
		t.Error("MCPJungle never called; test scaffold drift?")
	}
}
