package agentcontainer

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reconcileProxy is a httptest.Server tuned for reconcile: serves a
// configurable list response on GET /containers/json and records
// every Start / Stop / Remove call.
type reconcileProxy struct {
	server      *httptest.Server
	listResp    []containerJSON
	starts      []string
	stops       []string
	removes     []string
	startStatus int
}

func newReconcileProxy(t *testing.T) *reconcileProxy {
	t.Helper()
	p := &reconcileProxy{startStatus: http.StatusNoContent}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/json":
			w.WriteHeader(http.StatusOK)
			body, _ := json.Marshal(p.listResp)
			_, _ = w.Write(body)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/start")
			p.starts = append(p.starts, id)
			w.WriteHeader(p.startStatus)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/stop")
			p.stops = append(p.stops, id)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/containers/"):
			id := strings.TrimPrefix(r.URL.Path, "/containers/")
			p.removes = append(p.removes, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

// TestReconcileMatchesDockerPS — proxy reports 2 running containers,
// expected says both should run; report shows AdoptedRunning=2,
// no Restarted, no GarbageCollected.
func TestReconcileMatchesDockerPS(t *testing.T) {
	p := newReconcileProxy(t)
	p.listResp = []containerJSON{
		{ID: "c1", State: "running"},
		{ID: "c2", State: "running"},
	}
	c := NewSocketProxyController(p.server.URL, p.server.Client(), slog.New(slog.DiscardHandler))

	report, err := c.Reconcile(context.Background(), []ExpectedContainer{
		{AgentID: "a1", ContainerID: "c1", State: ExpectedRunning},
		{AgentID: "a2", ContainerID: "c2", State: ExpectedRunning},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.AdoptedRunning) != 2 {
		t.Errorf("AdoptedRunning=%v; want 2", report.AdoptedRunning)
	}
	if len(report.Restarted) != 0 || len(report.GarbageCollected) != 0 {
		t.Errorf("unexpected drift: %+v", report)
	}
	if len(p.starts) != 0 {
		t.Errorf("unexpected restarts: %v", p.starts)
	}
}

// TestReconcileRestartsStoppedContainer — proxy reports c1 stopped,
// expected says ExpectedRunning; reconcile issues POST /start.
func TestReconcileRestartsStoppedContainer(t *testing.T) {
	p := newReconcileProxy(t)
	p.listResp = []containerJSON{{ID: "c1", State: "exited"}}
	c := NewSocketProxyController(p.server.URL, p.server.Client(), slog.New(slog.DiscardHandler))

	report, err := c.Reconcile(context.Background(), []ExpectedContainer{
		{AgentID: "a1", ContainerID: "c1", State: ExpectedRunning},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Restarted) != 1 || report.Restarted[0] != "a1" {
		t.Errorf("Restarted=%v; want [a1]", report.Restarted)
	}
	if len(p.starts) != 1 || p.starts[0] != "c1" {
		t.Errorf("starts=%v; want [c1]", p.starts)
	}
}

// TestReconcileGCsOrphan — proxy reports a container whose ID isn't
// in the expected set; reconcile removes it.
func TestReconcileGCsOrphan(t *testing.T) {
	p := newReconcileProxy(t)
	p.listResp = []containerJSON{{ID: "orphan-1", State: "exited"}}
	c := NewSocketProxyController(p.server.URL, p.server.Client(), slog.New(slog.DiscardHandler))

	report, err := c.Reconcile(context.Background(), []ExpectedContainer{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.GarbageCollected) != 1 || report.GarbageCollected[0] != "orphan-1" {
		t.Errorf("GarbageCollected=%v; want [orphan-1]", report.GarbageCollected)
	}
	if len(p.removes) != 1 || p.removes[0] != "orphan-1" {
		t.Errorf("removes=%v; want [orphan-1]", p.removes)
	}
}

// TestReconcileMissingExpected — expected references a container the
// daemon doesn't know about; reconcile reports a Mismatch but doesn't
// fail.
func TestReconcileMissingExpected(t *testing.T) {
	p := newReconcileProxy(t)
	p.listResp = []containerJSON{} // empty
	c := NewSocketProxyController(p.server.URL, p.server.Client(), slog.New(slog.DiscardHandler))

	report, err := c.Reconcile(context.Background(), []ExpectedContainer{
		{AgentID: "a1", ContainerID: "c-vanished", State: ExpectedRunning},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("Mismatches=%v; want 1", report.Mismatches)
	}
	if report.Mismatches[0].ActualKind != "missing" {
		t.Errorf("Mismatch.ActualKind=%q; want missing", report.Mismatches[0].ActualKind)
	}
}
