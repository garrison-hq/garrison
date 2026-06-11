//go:build integration

package migrate7

// M7.1 T014 — boot-convergence integration test (SC-005, US4 AS-1/2/3).
// Replays the supervisor boot sequence (migrate7.Run + the shape
// reconcile walk that cmd/supervisor's unexported runBootShapeReconcile
// performs) against a testcontainers Postgres and an httptest fake
// docker daemon pre-seeded with the old-shape fleet: unlabeled (no
// garrison.shape_hash — pre-FR-007), Exited(1) (spike F1's claude
// entrypoint), one container per agent. The old shape's role-keyed
// workspace bind is invisible to the reconcile (it reads only labels),
// so the absent hash label is what represents "old shape" here.
//
// The fleet includes one hired agent — host_uid + image_digest set by
// the hire flow, last_grandfathered_at IS NULL forever (analyze C1) —
// pinning that the reconcile addresses it through the
// ListAgentsForContainerReconcile host_uid predicate, not the
// grandfathering timestamp.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	m71ImageRef      = "garrison-claude:m71int"
	m71ImageDigest   = "garrison-claude@sha256:m71bootconvergence"
	m71Network       = "garrison-agents"
	m71SupervisorBin = "/usr/local/bin/garrison-supervisor"
)

// -----------------------------------------------------------------------
// Fake docker daemon (httptest) — container-lifecycle endpoints only
// -----------------------------------------------------------------------

type m71FakeContainer struct {
	id      string
	name    string
	labels  map[string]string
	running bool
}

// m71FakeDaemon models the docker engine state the boot pass touches:
// image inspect, container create/inspect/start/stop/remove. It keeps
// two counters the SC-005 assertions read: applied (state changes that
// actually landed) and mutAttempts (mutating endpoints hit, including
// rejected ones — a 409 duplicate-name create is an attempt with zero
// applied mutations).
type m71FakeDaemon struct {
	mu          sync.Mutex
	seq         int
	containers  map[string]*m71FakeContainer // keyed by container ID
	applied     int
	mutAttempts int
}

func newM71FakeDaemon() *m71FakeDaemon {
	return &m71FakeDaemon{containers: make(map[string]*m71FakeContainer)}
}

// seedOldShape registers one pre-M7.1 container for agentID: managed +
// agent-ID labels but no shape-hash label, not running (the Exited(1)
// fleet). Returns the container ID so the test can assert replacement.
func (d *m71FakeDaemon) seedOldShape(agentID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seq++
	id := fmt.Sprintf("m71-old-%d", d.seq)
	d.containers[id] = &m71FakeContainer{
		id:   id,
		name: agentcontainer.ContainerName(agentID),
		labels: map[string]string{
			"garrison.agent_id": agentID,
			"garrison.managed":  "true",
		},
		running: false,
	}
	return id
}

// lockedResolve looks a container up by ID or name. Callers hold d.mu.
func (d *m71FakeDaemon) lockedResolve(nameOrID string) *m71FakeContainer {
	if c, ok := d.containers[nameOrID]; ok {
		return c
	}
	for _, c := range d.containers {
		if c.name == nameOrID {
			return c
		}
	}
	return nil
}

func (d *m71FakeDaemon) byName(name string) (m71FakeContainer, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.lockedResolve(name)
	if c == nil {
		return m71FakeContainer{}, false
	}
	out := *c
	out.labels = make(map[string]string, len(c.labels))
	for k, v := range c.labels {
		out.labels[k] = v
	}
	return out, true
}

func (d *m71FakeDaemon) appliedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.applied
}

func (d *m71FakeDaemon) mutAttemptCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mutAttempts
}

func (d *m71FakeDaemon) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && len(segs) == 3 && segs[0] == "images" && segs[2] == "json":
			_, _ = fmt.Fprintf(w, `{"Id":"sha256:m71-image-id","RepoDigests":[%q]}`, m71ImageDigest)
		case r.Method == http.MethodPost && len(segs) == 2 && segs[0] == "containers" && segs[1] == "create":
			name := r.URL.Query().Get("name")
			var body struct {
				Labels map[string]string `json:"Labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			d.mu.Lock()
			d.mutAttempts++
			if d.lockedResolve(name) != nil {
				d.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprintf(w, `{"message":"Conflict. The container name %q is already in use"}`, name)
				return
			}
			d.seq++
			id := fmt.Sprintf("m71-ctr-%d", d.seq)
			d.containers[id] = &m71FakeContainer{id: id, name: name, labels: body.Labels}
			d.applied++
			d.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, id)
		case r.Method == http.MethodGet && len(segs) == 3 && segs[0] == "containers" && segs[2] == "json":
			d.mu.Lock()
			c := d.lockedResolve(segs[1])
			if c == nil {
				d.mu.Unlock()
				http.NotFound(w, r)
				return
			}
			out := map[string]any{
				"Id":     c.id,
				"State":  map[string]any{"Running": c.running},
				"Config": map[string]any{"Labels": c.labels},
			}
			d.mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && len(segs) == 3 && segs[0] == "containers" && (segs[2] == "start" || segs[2] == "stop"):
			d.mu.Lock()
			d.mutAttempts++
			c := d.lockedResolve(segs[1])
			if c == nil {
				d.mu.Unlock()
				http.NotFound(w, r)
				return
			}
			wantRunning := segs[2] == "start"
			if c.running == wantRunning {
				d.mu.Unlock()
				w.WriteHeader(http.StatusNotModified)
				return
			}
			c.running = wantRunning
			d.applied++
			d.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && len(segs) == 2 && segs[0] == "containers":
			d.mu.Lock()
			d.mutAttempts++
			c := d.lockedResolve(segs[1])
			if c == nil {
				d.mu.Unlock()
				http.NotFound(w, r)
				return
			}
			delete(d.containers, c.id)
			d.applied++
			d.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

// -----------------------------------------------------------------------
// Seeding + boot-pass helpers
// -----------------------------------------------------------------------

func m71Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func m71SeedDepartment(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	var companyID, deptID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm71-reconcile-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, '/tmp/m71-reconcile')
		RETURNING id`,
		companyID,
	).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}
	return deptID
}

// m71SeedAgent inserts one container-owning agent. grandfathered=false
// is the hired shape: host_uid + image_digest populated by the hire
// flow, last_grandfathered_at NULL forever (analyze C1).
func m71SeedAgent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deptID pgtype.UUID, role string, hostUID int32, grandfathered bool) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (id, department_id, role_slug, agent_md, model, listens_for, status,
		                    host_uid, image_digest, last_grandfathered_at)
		VALUES (gen_random_uuid(), $1, $2, '# agent.md', 'claude-sonnet-4-5',
		        '["work.ticket.created.engineering.in_dev"]'::jsonb, 'active',
		        $3, $4, CASE WHEN $5 THEN now() END)
		RETURNING id`,
		deptID, role, hostUID, m71ImageDigest, grandfathered,
	).Scan(&id); err != nil {
		t.Fatalf("insert agent %s: %v", role, err)
	}
	return id
}

// m71BootPass replays buildAgentContainerRuntime's boot sequence:
// migrate7.Run (warn-and-continue — the never-grandfathered hired
// agent is re-listed every boot and its create 409-collides with the
// container the hire flow already made, so the boot degrades and the
// reconcile below converges it instead), then the shape-reconcile walk
// mirroring cmd/supervisor's unexported runBootShapeReconcile: list
// via the C1 query, MkdirAll workspaces, build specs through the
// single per-agent source, converge, write agent_container_events rows
// from the report. Returns the report plus the number of mutating
// docker calls the reconcile phase issued.
func m71BootPass(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	ctrl agentcontainer.Controller,
	daemon *m71FakeDaemon,
	workspaceFS, skillsFS string,
) (agentcontainer.ShapeReport, int) {
	t.Helper()
	q := store.New(pool)

	if err := Run(ctx, Deps{
		Pool:          pool,
		Queries:       q,
		Controller:    ctrl,
		Logger:        m71Logger(),
		ImageRef:      m71ImageRef,
		UIDStart:      1000,
		UIDEnd:        1999,
		WorkspaceFS:   workspaceFS,
		SkillsFS:      skillsFS,
		Memory:        "512m",
		CPUs:          "1.0",
		PIDsLimit:     200,
		NetworkName:   m71Network,
		SupervisorBin: m71SupervisorBin,
	}); err != nil {
		t.Logf("migrate7.Run degraded (warn-and-continue, mirrors buildAgentContainerRuntime): %v", err)
	}

	rows, err := q.ListAgentsForContainerReconcile(ctx)
	if err != nil {
		t.Fatalf("ListAgentsForContainerReconcile: %v", err)
	}
	specs := make([]agentcontainer.ContainerSpec, 0, len(rows))
	agentRows := make(map[string]store.ListAgentsForContainerReconcileRow, len(rows))
	for _, row := range rows {
		agentID := uuidString(row.ID)
		if agentID == "" || row.HostUid == nil || row.ImageDigest == nil || *row.ImageDigest == "" {
			t.Fatalf("reconcile row for %s missing host_uid/image_digest", row.RoleSlug)
		}
		spec := agentcontainer.SpecForAgent(agentcontainer.AgentSpecParams{
			AgentID:       agentID,
			RoleSlug:      row.RoleSlug,
			ImageDigest:   *row.ImageDigest,
			HostUID:       int(*row.HostUid),
			WorkspaceFS:   workspaceFS,
			SkillsFS:      skillsFS,
			NetworkName:   m71Network,
			SupervisorBin: m71SupervisorBin,
			Memory:        "512m",
			CPUs:          "1.0",
			PIDsLimit:     200,
		})
		if err := os.MkdirAll(spec.Workspace, 0o755); err != nil {
			t.Fatalf("mkdir workspace %s: %v", spec.Workspace, err)
		}
		specs = append(specs, spec)
		agentRows[agentID] = row
	}

	attemptsBefore := daemon.mutAttemptCount()
	report, err := ctrl.ReconcileShape(ctx, specs)
	if err != nil {
		t.Fatalf("ReconcileShape: %v", err)
	}
	reconcileAttempts := daemon.mutAttemptCount() - attemptsBefore

	writeEvent := func(agentID, kind string) {
		row, ok := agentRows[agentID]
		if !ok {
			t.Fatalf("report names unknown agent %s", agentID)
		}
		if _, err := q.InsertAgentContainerEvent(ctx, store.InsertAgentContainerEventParams{
			AgentID:     row.ID,
			Kind:        kind,
			ImageDigest: row.ImageDigest,
		}); err != nil {
			t.Fatalf("insert agent_container_events (%s, %s): %v", agentID, kind, err)
		}
	}
	for _, id := range report.Recreated {
		writeEvent(id, "removed")
		writeEvent(id, "created")
	}
	for _, id := range report.Created {
		writeEvent(id, "created")
	}
	for _, id := range report.Restarted {
		writeEvent(id, "started")
	}
	return report, reconcileAttempts
}

func m71SortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func m71EventKinds(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentID pgtype.UUID) map[string]int {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT kind, COUNT(*) FROM agent_container_events WHERE agent_id = $1 GROUP BY kind`, agentID)
	if err != nil {
		t.Fatalf("query agent_container_events: %v", err)
	}
	defer rows.Close()
	kinds := make(map[string]int)
	for rows.Next() {
		var kind string
		var n int64
		if err := rows.Scan(&kind, &n); err != nil {
			t.Fatalf("scan event kinds: %v", err)
		}
		kinds[kind] = int(n)
	}
	return kinds
}

func m71EventTotal(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM agent_container_events`).Scan(&n); err != nil {
		t.Fatalf("count agent_container_events: %v", err)
	}
	return int(n)
}

// -----------------------------------------------------------------------
// Test
// -----------------------------------------------------------------------

// TestBootConvergenceFromOldShapeFleet — SC-005 / US4 AS-1/2/3: one
// boot pass recreates every old-shape container (grandfathered and
// hired alike) to the new shape, leaves the fleet running, and writes
// a removed+created event-row pair per agent; a second boot pass
// reports all-Unchanged and applies zero container mutations.
func TestBootConvergenceFromOldShapeFleet(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	deptID := m71SeedDepartment(t, ctx, pool)

	// Three container-owning agents: two grandfathered at M7, one hired
	// (never-grandfathered — analyze C1).
	agents := map[string]pgtype.UUID{
		"engineer":    m71SeedAgent(t, ctx, pool, deptID, "engineer", 1000, true),
		"qa-engineer": m71SeedAgent(t, ctx, pool, deptID, "qa-engineer", 1001, true),
		"researcher":  m71SeedAgent(t, ctx, pool, deptID, "researcher", 1002, false),
	}
	hiredID := agents["researcher"]

	daemon := newM71FakeDaemon()
	oldContainerIDs := make(map[string]string, len(agents))
	wantAgentIDs := make([]string, 0, len(agents))
	for _, id := range agents {
		agentID := uuidString(id)
		oldContainerIDs[agentID] = daemon.seedOldShape(agentID)
		wantAgentIDs = append(wantAgentIDs, agentID)
	}
	sort.Strings(wantAgentIDs)

	srv := httptest.NewServer(daemon.handler())
	defer srv.Close()
	ctrl := agentcontainer.NewSocketProxyController(srv.URL, nil, m71Logger())
	workspaceFS, skillsFS := t.TempDir(), t.TempDir()

	// --- Boot pass 1: the whole fleet converges to the new shape.
	report1, attempts1 := m71BootPass(t, ctx, pool, ctrl, daemon, workspaceFS, skillsFS)
	if got := m71SortedCopy(report1.Recreated); !equalStrings(got, wantAgentIDs) {
		t.Fatalf("pass 1 Recreated = %v; want all of %v", got, wantAgentIDs)
	}
	if len(report1.Created)+len(report1.Restarted)+len(report1.Unchanged) != 0 {
		t.Errorf("pass 1 report has non-Recreated entries: %+v", report1)
	}
	// Stop + remove + create + start per recreated agent.
	if want := 4 * len(agents); attempts1 != want {
		t.Errorf("pass 1 reconcile mutating calls = %d; want %d", attempts1, want)
	}

	for agentID, oldID := range oldContainerIDs {
		c, ok := daemon.byName(agentcontainer.ContainerName(agentID))
		if !ok {
			t.Fatalf("agent %s has no container after pass 1", agentID)
		}
		if c.id == oldID {
			t.Errorf("agent %s still runs the old-shape container %s", agentID, oldID)
		}
		if !c.running {
			t.Errorf("agent %s container not running after pass 1", agentID)
		}
		if c.labels["garrison.shape_hash"] == "" {
			t.Errorf("agent %s container carries no shape-hash label after pass 1", agentID)
		}
	}

	// Event-row pairs: removed + created per agent, nothing else (the
	// hired agent's failed grandfather attempt rolled back, so no
	// 'migrated' row exists either).
	for role, id := range agents {
		kinds := m71EventKinds(t, ctx, pool, id)
		if kinds["removed"] != 1 || kinds["created"] != 1 || len(kinds) != 2 {
			t.Errorf("%s event kinds = %v; want exactly {removed:1 created:1}", role, kinds)
		}
	}

	// The hired agent converged without ever being grandfathered: the
	// reconcile addressed it via host_uid IS NOT NULL (analyze C1).
	var lastGrandfathered pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT last_grandfathered_at FROM agents WHERE id = $1`, hiredID,
	).Scan(&lastGrandfathered); err != nil {
		t.Fatalf("read hired agent: %v", err)
	}
	if lastGrandfathered.Valid {
		t.Errorf("hired agent gained last_grandfathered_at; the C1 fixture must stay never-grandfathered")
	}

	// --- Boot pass 2: all-Unchanged, zero container mutations (SC-005).
	appliedBefore := daemon.appliedCount()
	eventsBefore := m71EventTotal(t, ctx, pool)

	report2, attempts2 := m71BootPass(t, ctx, pool, ctrl, daemon, workspaceFS, skillsFS)
	if got := m71SortedCopy(report2.Unchanged); !equalStrings(got, wantAgentIDs) {
		t.Fatalf("pass 2 Unchanged = %v; want all of %v", got, wantAgentIDs)
	}
	if len(report2.Created)+len(report2.Recreated)+len(report2.Restarted) != 0 {
		t.Errorf("pass 2 report mutated containers: %+v", report2)
	}
	if attempts2 != 0 {
		t.Errorf("pass 2 reconcile issued %d mutating docker calls; want 0", attempts2)
	}
	if applied := daemon.appliedCount(); applied != appliedBefore {
		t.Errorf("pass 2 applied %d container mutations; want 0 (SC-005)", applied-appliedBefore)
	}
	if events := m71EventTotal(t, ctx, pool); events != eventsBefore {
		t.Errorf("pass 2 wrote %d agent_container_events rows; want 0", events-eventsBefore)
	}

	// The converged fleet is still running the pass-1 containers.
	for agentID := range oldContainerIDs {
		c, ok := daemon.byName(agentcontainer.ContainerName(agentID))
		if !ok || !c.running {
			t.Errorf("agent %s container missing or stopped after pass 2", agentID)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
