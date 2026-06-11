package agentcontainer

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// shapeProxy is an httptest.Server tuned for ReconcileShape: serves
// scripted GET /containers/<name>/json responses (absent name → 404),
// answers the mutating endpoints, and records every request so tests
// can assert exactly which containers were touched.
type shapeProxy struct {
	server *httptest.Server

	mu       sync.Mutex
	inspect  map[string]containerInspectJSON // keyed by container name
	requests []string                        // "METHOD path" in order
	creates  []string                        // ?name= values
	starts   []string
	stops    []string
	removes  []string
	nextNew  int
}

func newShapeProxy(t *testing.T) *shapeProxy {
	t.Helper()
	p := &shapeProxy{inspect: map[string]containerInspectJSON{}}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.requests = append(p.requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
			resp, ok := p.inspect[name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			body, _ := json.Marshal(resp)
			_, _ = w.Write(body)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			p.creates = append(p.creates, r.URL.Query().Get("name"))
			p.nextNew++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"new-` + strconv.Itoa(p.nextNew) + `"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			p.starts = append(p.starts, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/start"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/stop"):
			p.stops = append(p.stops, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/stop"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/containers/"):
			p.removes = append(p.removes, strings.SplitN(strings.TrimPrefix(r.URL.Path, "/containers/"), "?", 2)[0])
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *shapeProxy) controller(t *testing.T) Controller {
	t.Helper()
	return NewSocketProxyController(p.server.URL, p.server.Client(), slog.New(slog.DiscardHandler))
}

// shapeSpec builds a fully-populated ContainerSpec for the given agent
// UUID — the same field set SpecForAgent would emit.
func shapeSpec(agentID string) ContainerSpec {
	return ContainerSpec{
		AgentID:       agentID,
		Image:         "garrison-claude@sha256:abc123",
		HostUID:       11000,
		Workspace:     "/var/lib/garrison/workspaces/" + agentID,
		Skills:        "/var/lib/garrison/skills/engineer",
		NetworkName:   "garrison-agents",
		SupervisorBin: "/usr/local/bin/garrison-supervisor",
		Memory:        "512m",
		CPUs:          "1.0",
		PIDsLimit:     200,
	}
}

func mustShapeHash(t *testing.T, spec ContainerSpec) string {
	t.Helper()
	body, err := buildCreateBody(spec)
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}
	return body.Labels[shapeHashLabel]
}

func matchingInspect(id, hash string, running bool) containerInspectJSON {
	out := containerInspectJSON{ID: id}
	out.State.Running = running
	out.Config.Labels = map[string]string{
		"garrison.managed": "true",
		shapeHashLabel:     hash,
	}
	return out
}

// TestReconcileShapeRecreatesOnMissingOrStaleLabel — US4 AS-1: the
// live Exited(1) fleet carries no garrison.shape_hash label at all; a
// container with a stale hash is equally old-shape. Both are stopped,
// removed, recreated, and started.
func TestReconcileShapeRecreatesOnMissingOrStaleLabel(t *testing.T) {
	agentUnlabeled := "11111111-2222-3333-4444-555555555555"
	agentStale := "22222222-3333-4444-5555-666666666666"
	p := newShapeProxy(t)

	// Old M7 shape: managed label present, no shape hash, exited.
	unlabeled := containerInspectJSON{ID: "old-unlabeled"}
	unlabeled.Config.Labels = map[string]string{"garrison.managed": "true"}
	p.inspect[ContainerName(agentUnlabeled)] = unlabeled
	// Stale hash, even running, still recreates.
	p.inspect[ContainerName(agentStale)] = matchingInspect("old-stale", "deadbeef", true)

	report, err := p.controller(t).ReconcileShape(context.Background(),
		[]ContainerSpec{shapeSpec(agentUnlabeled), shapeSpec(agentStale)})
	if err != nil {
		t.Fatalf("ReconcileShape: %v", err)
	}
	if len(report.Recreated) != 2 || report.Recreated[0] != agentUnlabeled || report.Recreated[1] != agentStale {
		t.Errorf("Recreated=%v; want [%s %s]", report.Recreated, agentUnlabeled, agentStale)
	}
	if len(report.Created)+len(report.Restarted)+len(report.Unchanged) != 0 {
		t.Errorf("unexpected non-recreate outcomes: %+v", report)
	}
	if len(p.removes) != 2 || p.removes[0] != "old-unlabeled" || p.removes[1] != "old-stale" {
		t.Errorf("removes=%v; want [old-unlabeled old-stale]", p.removes)
	}
	if len(p.creates) != 2 || p.creates[0] != ContainerName(agentUnlabeled) || p.creates[1] != ContainerName(agentStale) {
		t.Errorf("creates=%v; want the two agent container names", p.creates)
	}
	if len(p.starts) != 2 {
		t.Errorf("starts=%v; want 2 (one per recreated container)", p.starts)
	}
}

// TestReconcileShapeNoopWhenHashMatches — US4 AS-2: a running container
// whose shape hash matches is reported Unchanged with zero mutations
// (SC-005 at unit level).
func TestReconcileShapeNoopWhenHashMatches(t *testing.T) {
	agentID := "33333333-4444-5555-6666-777777777777"
	spec := shapeSpec(agentID)
	p := newShapeProxy(t)
	p.inspect[ContainerName(agentID)] = matchingInspect("c-match", mustShapeHash(t, spec), true)

	report, err := p.controller(t).ReconcileShape(context.Background(), []ContainerSpec{spec})
	if err != nil {
		t.Fatalf("ReconcileShape: %v", err)
	}
	if len(report.Unchanged) != 1 || report.Unchanged[0] != agentID {
		t.Errorf("Unchanged=%v; want [%s]", report.Unchanged, agentID)
	}
	if len(p.creates)+len(p.starts)+len(p.stops)+len(p.removes) != 0 {
		t.Errorf("expected zero container mutations; got creates=%v starts=%v stops=%v removes=%v",
			p.creates, p.starts, p.stops, p.removes)
	}
}

// TestReconcileShapeCreatesMissingContainer — US4 AS-3: no container
// for the agent at all → create + start, reported Created.
func TestReconcileShapeCreatesMissingContainer(t *testing.T) {
	agentID := "44444444-5555-6666-7777-888888888888"
	p := newShapeProxy(t)

	report, err := p.controller(t).ReconcileShape(context.Background(),
		[]ContainerSpec{shapeSpec(agentID)})
	if err != nil {
		t.Fatalf("ReconcileShape: %v", err)
	}
	if len(report.Created) != 1 || report.Created[0] != agentID {
		t.Errorf("Created=%v; want [%s]", report.Created, agentID)
	}
	if len(p.creates) != 1 || p.creates[0] != ContainerName(agentID) {
		t.Errorf("creates=%v; want [%s]", p.creates, ContainerName(agentID))
	}
	if len(p.starts) != 1 {
		t.Errorf("starts=%v; want 1", p.starts)
	}
	if len(p.stops)+len(p.removes) != 0 {
		t.Errorf("unexpected stop/remove on missing-container path: stops=%v removes=%v", p.stops, p.removes)
	}
}

// TestReconcileShapeStartsStoppedMatchingContainer — hash matches but
// the container is stopped: start it in place, no recreate.
func TestReconcileShapeStartsStoppedMatchingContainer(t *testing.T) {
	agentID := "55555555-6666-7777-8888-999999999999"
	spec := shapeSpec(agentID)
	p := newShapeProxy(t)
	p.inspect[ContainerName(agentID)] = matchingInspect("c-stopped", mustShapeHash(t, spec), false)

	report, err := p.controller(t).ReconcileShape(context.Background(), []ContainerSpec{spec})
	if err != nil {
		t.Fatalf("ReconcileShape: %v", err)
	}
	if len(report.Restarted) != 1 || report.Restarted[0] != agentID {
		t.Errorf("Restarted=%v; want [%s]", report.Restarted, agentID)
	}
	if len(p.starts) != 1 || p.starts[0] != "c-stopped" {
		t.Errorf("starts=%v; want [c-stopped]", p.starts)
	}
	if len(p.creates)+len(p.stops)+len(p.removes) != 0 {
		t.Errorf("unexpected mutations beyond start: creates=%v stops=%v removes=%v",
			p.creates, p.stops, p.removes)
	}
}

// TestReconcileShapeNeverTouchesForeignContainers — the reconcile only
// addresses containers by the agent-ID name; the chat container and
// other compose services are never inspected, let alone mutated.
func TestReconcileShapeNeverTouchesForeignContainers(t *testing.T) {
	agentID := "66666666-7777-8888-9999-aaaaaaaaaaaa"
	spec := shapeSpec(agentID)
	p := newShapeProxy(t)
	p.inspect[ContainerName(agentID)] = matchingInspect("c-agent", mustShapeHash(t, spec), true)
	// Foreign containers the daemon knows about; reconcile must not
	// address them.
	foreign := containerInspectJSON{ID: "c-chat"}
	p.inspect["garrison-chat"] = foreign
	p.inspect["garrison-egress-proxy"] = containerInspectJSON{ID: "c-egress"}

	if _, err := p.controller(t).ReconcileShape(context.Background(), []ContainerSpec{spec}); err != nil {
		t.Fatalf("ReconcileShape: %v", err)
	}
	for _, req := range p.requests {
		if strings.Contains(req, "garrison-chat") || strings.Contains(req, "garrison-egress-proxy") {
			t.Errorf("foreign container addressed: %s", req)
		}
		if !strings.Contains(req, ContainerName(agentID)) {
			t.Errorf("request to non-agent path: %s", req)
		}
	}
	if len(p.stops)+len(p.removes)+len(p.creates)+len(p.starts) != 0 {
		t.Errorf("expected zero mutations; got creates=%v starts=%v stops=%v removes=%v",
			p.creates, p.starts, p.stops, p.removes)
	}
}
