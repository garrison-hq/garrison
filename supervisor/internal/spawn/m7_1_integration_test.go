//go:build integration

package spawn

// M7.1 T013 — golden-path integration suite (the milestone's smoke
// test). Full Spawn runs against a testcontainers Postgres while an
// httptest fake docker proxy plays the socket-proxy role: exec-create /
// exec-start / exec-inspect / restart, with the claude exec's stdout
// streamed back as canned claudeproto NDJSON in the 8-byte raw-frame
// encoding (spike F2). The production socketProxyController is the
// system under test end-to-end — only the docker daemon and claude
// itself are canned.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// -----------------------------------------------------------------------
// Canned claudeproto streams
// -----------------------------------------------------------------------

// m71IntGoldenStream is the healthy end-to-end NDJSON feed: init frame
// with every container-path MCP server connected and cwd /workspace
// (US1/SC-001), one successful finalize_ticket tool_use/tool_result
// pair, then the terminal result frame. The finalize payload satisfies
// every finalize.Validate constraint (outcome ≥ 10, rationale ≥ 50,
// ≥ 1 kg triple).
func m71IntGoldenStream(ticketID string) string {
	initFrame := `{"type":"system","subtype":"init","session_id":"sess-m71-int","cwd":"/workspace","mcp_servers":[{"name":"postgres","status":"connected"},{"name":"finalize","status":"connected"},{"name":"garrison-mutate","status":"connected"}]}`
	assistant := fmt.Sprintf(
		`{"type":"assistant","message":{"model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_m71_int","name":"finalize_ticket","input":{"ticket_id":"%s","outcome":"Container pipeline integration run committed the deliverable","diary_entry":{"rationale":"The canned engineer run finished the ticket work and exercised the full finalize path through the container transport for the M7.1 golden-path suite.","artifacts":["changes/m71.md"],"blockers":[],"discoveries":[]},"kg_triples":[{"subject":"agent_instance_m71","predicate":"completed","object":"ticket_%s","valid_from":"now"}]}}]}}`,
		ticketID, ticketID,
	)
	toolResult := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_m71_int","is_error":false,"content":[{"type":"text","text":"{\"ok\":true,\"attempt\":1}"}]}]}}`
	result := `{"type":"result","subtype":"success","is_error":false,"duration_ms":900,"total_cost_usd":0.014,"stop_reason":"end_turn","session_id":"sess-m71-int"}`
	return initFrame + "\n" + assistant + "\n" + toolResult + "\n" + result + "\n"
}

// m71IntFailedMCPStream is an init frame whose postgres server failed —
// the FR-108 gate input. Nothing follows: the pipeline bails at init.
const m71IntFailedMCPStream = `{"type":"system","subtype":"init","session_id":"sess-m71-int","cwd":"/workspace","mcp_servers":[{"name":"postgres","status":"failed"},{"name":"finalize","status":"connected"},{"name":"garrison-mutate","status":"connected"}]}` + "\n"

// rawNDJSONFrames encodes an NDJSON stream as docker raw-stream frames
// (one [stream=1 pad(3) size(4)] header per line — spike F2), the wire
// shape the production demux consumes off the exec-start response.
func rawNDJSONFrames(ndjson string) []byte {
	var buf bytes.Buffer
	for _, line := range strings.SplitAfter(ndjson, "\n") {
		if line == "" {
			continue
		}
		header := make([]byte, 8)
		header[0] = 1 // stdout
		binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
		buf.Write(header)
		buf.WriteString(line)
	}
	return buf.Bytes()
}

// -----------------------------------------------------------------------
// Fake docker proxy (httptest) — the socket-proxy stand-in
// -----------------------------------------------------------------------

// m71ExecRecord captures one exec-create POST: which container it
// addressed and the body the controller sent.
type m71ExecRecord struct {
	Container  string
	Cmd        []string
	Env        []string
	WorkingDir string
}

// m71FakeDockerProxy implements the four docker engine endpoints the
// container spawn path exercises: exec-create, exec-start (raw-frame
// body), exec-inspect, and container restart. The claude exec (argv[0]
// /usr/bin/timeout — the FR-016 wrapper) gets the canned frame stream
// and the scripted exit code; helper execs (config write, rm) get an
// empty stream and exit 0.
type m71FakeDockerProxy struct {
	mu           sync.Mutex
	seq          int
	claudeExecs  map[string]bool // exec id → is the timeout-wrapped claude exec
	execs        []m71ExecRecord
	restarts     []string
	claudeFrames []byte
	claudeExit   int
}

func newM71FakeDockerProxy(stream string, claudeExit int) *m71FakeDockerProxy {
	return &m71FakeDockerProxy{
		claudeExecs:  make(map[string]bool),
		claudeFrames: rawNDJSONFrames(stream),
		claudeExit:   claudeExit,
	}
}

func (p *m71FakeDockerProxy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodPost && len(segs) == 3 && segs[0] == "containers" && segs[2] == "exec":
			var body struct {
				Cmd        []string `json:"Cmd"`
				Env        []string `json:"Env"`
				WorkingDir string   `json:"WorkingDir"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad exec-create body", http.StatusBadRequest)
				return
			}
			p.mu.Lock()
			p.seq++
			id := fmt.Sprintf("m71-exec-%d", p.seq)
			p.claudeExecs[id] = len(body.Cmd) > 0 && body.Cmd[0] == "/usr/bin/timeout"
			p.execs = append(p.execs, m71ExecRecord{
				Container:  segs[1],
				Cmd:        body.Cmd,
				Env:        body.Env,
				WorkingDir: body.WorkingDir,
			})
			p.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, id)
		case r.Method == http.MethodPost && len(segs) == 3 && segs[0] == "exec" && segs[2] == "start":
			p.mu.Lock()
			isClaude := p.claudeExecs[segs[1]]
			frames := p.claudeFrames
			p.mu.Unlock()
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
			if isClaude {
				_, _ = w.Write(frames)
			}
		case r.Method == http.MethodGet && len(segs) == 3 && segs[0] == "exec" && segs[2] == "json":
			p.mu.Lock()
			code := 0
			if p.claudeExecs[segs[1]] {
				code = p.claudeExit
			}
			p.mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, code)
		case r.Method == http.MethodPost && len(segs) == 3 && segs[0] == "containers" && segs[2] == "restart":
			p.mu.Lock()
			p.restarts = append(p.restarts, segs[1])
			p.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func (p *m71FakeDockerProxy) execRecords() []m71ExecRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]m71ExecRecord(nil), p.execs...)
}

func (p *m71FakeDockerProxy) restartTargets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.restarts...)
}

// -----------------------------------------------------------------------
// Fake palace (dockerexec seam) — WriteFinalize's MemPalace collaborator
// -----------------------------------------------------------------------

// m71FakePalaceExec satisfies dockerexec.DockerExec with canned JSON-RPC
// responses: id=1 (initialize) + id=2 (the single tools/call) — the
// shape both AddDrawer and the one-triple AddTriples expect.
type m71FakePalaceExec struct {
	mu    sync.Mutex
	calls int
}

func (f *m71FakePalaceExec) Run(_ context.Context, _ []string, stdin io.Reader) ([]byte, []byte, error) {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return []byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":{}}` + "\n"), nil, nil
}

func (f *m71FakePalaceExec) RunStream(
	_ context.Context,
	_ []string,
	_ func(stdin io.WriteCloser) error,
	_ func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	return nil, errors.New("m71FakePalaceExec: RunStream not implemented")
}

func (f *m71FakePalaceExec) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// -----------------------------------------------------------------------
// Seeding + deps
// -----------------------------------------------------------------------

// seedM71Fixture inserts the company → department → agents chain the
// container spawn path resolves at prepare time. The agent's
// palace_wing stays NULL so the wake-up step is skipped (both modes
// identically) and wake_up_status lands as 'skipped'.
func seedM71Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspacePath string) (deptID, agentID pgtype.UUID) {
	t.Helper()
	var companyID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO companies (id, name) VALUES (gen_random_uuid(), 'm71-integration-co') RETURNING id`,
	).Scan(&companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, $2)
		RETURNING id`,
		companyID, workspacePath,
	).Scan(&deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agents (id, department_id, role_slug, agent_md, model, listens_for, status)
		VALUES (gen_random_uuid(), $1, 'engineer', '# engineer agent.md', 'claude-sonnet-4-5',
		        '["work.ticket.created.engineering.in_dev"]'::jsonb, 'active')
		RETURNING id`,
		deptID,
	).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return deptID, agentID
}

// seedM71TicketEvent inserts one in_dev ticket plus its
// work.ticket.created event row and returns both IDs.
func seedM71TicketEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deptID pgtype.UUID) (eventID, ticketID pgtype.UUID) {
	t.Helper()
	if err := pool.QueryRow(ctx, `
		INSERT INTO tickets (id, department_id, objective, column_slug)
		VALUES (gen_random_uuid(), $1, 'M7.1 golden-path integration ticket', 'in_dev')
		RETURNING id`,
		deptID,
	).Scan(&ticketID); err != nil {
		t.Fatalf("insert ticket: %v", err)
	}
	payload := fmt.Sprintf(
		`{"ticket_id":"%s","department_id":"%s","column_slug":"in_dev"}`,
		uuidString(ticketID), uuidString(deptID),
	)
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.ticket.created.engineering.in_dev', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return eventID, ticketID
}

// m71IntegrationDeps builds the shared Deps both modes start from. The
// AgentsCache loads after seeding; the palace client rides the fake
// dockerexec seam so WriteFinalize's drawer + triple writes succeed
// without a MemPalace sidecar.
func m71IntegrationDeps(t *testing.T, ctx context.Context, pool *pgxpool.Pool, palaceExec *m71FakePalaceExec) Deps {
	t.Helper()
	cache, err := agents.NewCache(ctx, store.New(pool))
	if err != nil {
		t.Fatalf("agents.NewCache: %v", err)
	}
	return Deps{
		Pool:              pool,
		Queries:           store.New(pool),
		Logger:            testLogger(),
		SubprocessTimeout: 60 * time.Second,
		AgentsCache:       cache,
		ClaudeModel:       "claude-sonnet-4-5",
		ClaudeBudgetUSD:   0.10,
		SupervisorBin:     "/usr/local/bin/garrison-supervisor",
		AgentRODSN:        "postgres://garrison_agent_ro:pw@garrison-postgres:5432/garrison",
		DatabaseURL:       "postgres://garrison:pw@garrison-postgres:5432/garrison",
		Palace: &mempalace.Client{
			DockerBin:          "/usr/bin/docker",
			MempalaceContainer: "garrison-mempalace",
			PalacePath:         "/palace",
			Timeout:            5 * time.Second,
			Exec:               palaceExec,
		},
		FinalizeWriteTimeout: 10 * time.Second,
		Inflight:             NewAgentInflight(),
		EgressProxyURL:       "http://garrison-egress-proxy:3128",
		AgentWorkspaceFS:     t.TempDir(),
	}
}

// m71ContainerDeps flips the shared deps into container mode against
// the fake proxy: the production socketProxyController is the transport.
func m71ContainerDeps(t *testing.T, base Deps, proxyURL string) Deps {
	t.Helper()
	base.UseDirectExec = false
	base.AgentContainer = agentcontainer.NewSocketProxyController(proxyURL, nil, testLogger())
	return base
}

// m71InstanceRow is the terminal-contract projection both modes share.
type m71InstanceRow struct {
	ID           pgtype.UUID
	Status       string
	ExitReason   *string
	Pid          *int32
	WakeUpStatus *string
	CostNull     bool
	FinishedNull bool
}

func m71ReadInstance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) m71InstanceRow {
	t.Helper()
	var row m71InstanceRow
	var cost pgtype.Numeric
	var finished pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT id, status, exit_reason, pid, wake_up_status, total_cost_usd, finished_at
		FROM agent_instances WHERE ticket_id = $1`,
		ticketID,
	).Scan(&row.ID, &row.Status, &row.ExitReason, &row.Pid, &row.WakeUpStatus, &cost, &finished); err != nil {
		t.Fatalf("read agent_instances: %v", err)
	}
	row.CostNull = !cost.Valid
	row.FinishedNull = !finished.Valid
	return row
}

// -----------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------

// TestContainerPipelineEndToEnd — US1 AS-1/2/3: a full Spawn with
// UseDirectExec=false runs the claude exec through the (fake) docker
// proxy, the healthy init frame passes the FR-108 gate, the finalize
// tool_use commits the atomic write, the kanban transition lands, and
// the terminal agent_instances row matches the direct-exec contract.
func TestContainerPipelineEndToEnd(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	deptID, agentID := seedM71Fixture(t, ctx, pool, filepath.Join(t.TempDir(), "engineering"))
	eventID, ticketID := seedM71TicketEvent(t, ctx, pool, deptID)

	proxy := newM71FakeDockerProxy(m71IntGoldenStream(uuidString(ticketID)), 0)
	srv := httptest.NewServer(proxy.handler())
	defer srv.Close()

	palaceExec := &m71FakePalaceExec{}
	deps := m71ContainerDeps(t, m71IntegrationDeps(t, ctx, pool, palaceExec), srv.URL)

	if err := Spawn(ctx, deps, eventID, "engineer"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Terminal row: the finalize atomic commit's contract (US1 AS-3).
	row := m71ReadInstance(t, ctx, pool, ticketID)
	if row.Status != "succeeded" || row.ExitReason == nil || *row.ExitReason != ExitCompleted {
		t.Errorf("terminal = (%s, %v); want (succeeded, %s)", row.Status, row.ExitReason, ExitCompleted)
	}
	if row.WakeUpStatus == nil || *row.WakeUpStatus != "skipped" {
		t.Errorf("wake_up_status = %v; want skipped (no palace wing configured)", row.WakeUpStatus)
	}
	if !row.CostNull {
		t.Errorf("total_cost_usd populated; commit fires before the result frame so cost stays NULL (direct-exec contract)")
	}
	if row.FinishedNull {
		t.Errorf("finished_at is NULL; the atomic commit writes the terminal timestamp")
	}
	if row.Pid != nil {
		t.Errorf("pid = %v; container path never records the host-namespace exec PID", *row.Pid)
	}

	// Kanban transition (US1 AS-2): in_dev → qa_review, hygiene clean.
	var columnSlug string
	if err := pool.QueryRow(ctx, `SELECT column_slug FROM tickets WHERE id = $1`, ticketID).Scan(&columnSlug); err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if columnSlug != "qa_review" {
		t.Errorf("ticket column_slug = %q; want qa_review", columnSlug)
	}
	var fromCol, hygiene *string
	var toCol string
	if err := pool.QueryRow(ctx, `
		SELECT from_column, to_column, hygiene_status FROM ticket_transitions
		WHERE ticket_id = $1 AND triggered_by_agent_instance_id = $2`,
		ticketID, row.ID,
	).Scan(&fromCol, &toCol, &hygiene); err != nil {
		t.Fatalf("read ticket_transitions: %v", err)
	}
	if fromCol == nil || *fromCol != "in_dev" || toCol != "qa_review" {
		t.Errorf("transition = (%v → %s); want (in_dev → qa_review)", fromCol, toCol)
	}
	if hygiene == nil || *hygiene != FinalizeDiaryHygieneStatus {
		t.Errorf("transition hygiene_status = %v; want %s", hygiene, FinalizeDiaryHygieneStatus)
	}

	// Event marked processed inside the atomic tx.
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `SELECT processed_at FROM event_outbox WHERE id = $1`, eventID).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Errorf("event_outbox.processed_at is NULL; finalize commit must mark the event processed")
	}

	// Transport shape: three execs (config write, timeout-wrapped
	// claude with cwd /workspace, rm cleanup), all addressed to the
	// agent-ID-keyed container name (FR-008), zero restarts.
	wantName := agentcontainer.ContainerName(uuidString(agentID))
	execs := proxy.execRecords()
	if len(execs) != 3 {
		t.Fatalf("exec-create calls = %d; want 3 (config write, claude, rm): %+v", len(execs), execs)
	}
	for i, e := range execs {
		if e.Container != wantName {
			t.Errorf("exec %d container = %q; want %q", i, e.Container, wantName)
		}
	}
	if execs[0].Cmd[0] != "/bin/sh" {
		t.Errorf("exec 0 argv[0] = %q; want the /bin/sh config-write helper", execs[0].Cmd[0])
	}
	if execs[1].Cmd[0] != "/usr/bin/timeout" || execs[1].WorkingDir != "/workspace" {
		t.Errorf("claude exec = (argv[0] %q, cwd %q); want (/usr/bin/timeout, /workspace)", execs[1].Cmd[0], execs[1].WorkingDir)
	}
	if execs[2].Cmd[0] != "/bin/rm" {
		t.Errorf("exec 2 argv[0] = %q; want the /bin/rm cleanup helper", execs[2].Cmd[0])
	}
	if n := len(proxy.restartTargets()); n != 0 {
		t.Errorf("restarts = %d; want 0 on the healthy path", n)
	}

	// MemPalace writes: AddDrawer + AddTriples = two docker-exec calls.
	if n := palaceExec.callCount(); n != 2 {
		t.Errorf("palace exec calls = %d; want 2 (AddDrawer + AddTriples)", n)
	}
}

// TestContainerPipelineMCPGateBails — FR-108 carried onto the container
// transport (FR-013): an init frame with a failed server bails the
// pipeline, the bail hook restarts the container (the SIGKILL analog),
// and the terminal row keeps the existing mcp_<server>_<status>
// vocabulary with no kanban movement.
func TestContainerPipelineMCPGateBails(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	deptID, agentID := seedM71Fixture(t, ctx, pool, filepath.Join(t.TempDir(), "engineering"))
	eventID, ticketID := seedM71TicketEvent(t, ctx, pool, deptID)

	// Exit 137: the restart kills the in-flight exec (KILL-shaped),
	// which the bail latch suppresses from Signaled classification.
	proxy := newM71FakeDockerProxy(m71IntFailedMCPStream, 137)
	srv := httptest.NewServer(proxy.handler())
	defer srv.Close()

	palaceExec := &m71FakePalaceExec{}
	deps := m71ContainerDeps(t, m71IntegrationDeps(t, ctx, pool, palaceExec), srv.URL)

	if err := Spawn(ctx, deps, eventID, "engineer"); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	row := m71ReadInstance(t, ctx, pool, ticketID)
	if row.Status != "failed" || row.ExitReason == nil || *row.ExitReason != "mcp_postgres_failed" {
		t.Errorf("terminal = (%s, %v); want (failed, mcp_postgres_failed)", row.Status, row.ExitReason)
	}

	// The bail hook restarted the agent's container.
	wantName := agentcontainer.ContainerName(uuidString(agentID))
	restarts := proxy.restartTargets()
	if len(restarts) == 0 {
		t.Fatalf("Restart was never invoked on the MCP-gate bail")
	}
	for i, name := range restarts {
		if name != wantName {
			t.Errorf("restart %d target = %q; want %q", i, name, wantName)
		}
	}

	// No kanban movement, no palace writes; event processed (the
	// MCP-gate terminal follows the direct-exec mark-processed contract).
	var columnSlug string
	if err := pool.QueryRow(ctx, `SELECT column_slug FROM tickets WHERE id = $1`, ticketID).Scan(&columnSlug); err != nil {
		t.Fatalf("read ticket: %v", err)
	}
	if columnSlug != "in_dev" {
		t.Errorf("ticket column_slug = %q; want in_dev (no transition on bail)", columnSlug)
	}
	var transitions int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM ticket_transitions WHERE ticket_id = $1`, ticketID).Scan(&transitions); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if transitions != 0 {
		t.Errorf("ticket_transitions rows = %d; want 0", transitions)
	}
	if n := palaceExec.callCount(); n != 0 {
		t.Errorf("palace exec calls = %d; want 0 on the bail path", n)
	}
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `SELECT processed_at FROM event_outbox WHERE id = $1`, eventID).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if !processed.Valid {
		t.Errorf("event_outbox.processed_at is NULL; the MCP-bail terminal marks the event processed")
	}
}

// TestBothModesProduceIdenticalRowShape — US3 AS-2 / SC-004 row-shape
// half: the same canned stream run under each flag value produces
// agent_instances rows with identical populated/NULL column sets. The
// single documented exception is pid (plan §5: the container path
// skips UpdatePID because the exec's PID is host-namespace; the column
// is nullable so the row shape is unchanged) — pinned explicitly below
// rather than excluded silently.
func TestBothModesProduceIdenticalRowShape(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	deptID, _ := seedM71Fixture(t, ctx, pool, filepath.Join(t.TempDir(), "engineering"))
	containerEvent, containerTicket := seedM71TicketEvent(t, ctx, pool, deptID)
	directEvent, directTicket := seedM71TicketEvent(t, ctx, pool, deptID)

	palaceExec := &m71FakePalaceExec{}
	base := m71IntegrationDeps(t, ctx, pool, palaceExec)

	// Leg 1: container mode against the fake docker proxy.
	proxy := newM71FakeDockerProxy(m71IntGoldenStream(uuidString(containerTicket)), 0)
	srv := httptest.NewServer(proxy.handler())
	defer srv.Close()
	containerDeps := m71ContainerDeps(t, base, srv.URL)
	if err := Spawn(ctx, containerDeps, containerEvent, "engineer"); err != nil {
		t.Fatalf("container-mode Spawn: %v", err)
	}

	// Leg 2: direct-exec mode with a fake claude binary emitting the
	// same canned stream on stdout.
	directDeps := base
	directDeps.UseDirectExec = true
	directDeps.AgentContainer = nil
	directDeps.ClaudeBin = writeM71FakeClaude(t, m71IntGoldenStream(uuidString(directTicket)))
	directDeps.MCPConfigDir = t.TempDir()
	if err := Spawn(ctx, directDeps, directEvent, "engineer"); err != nil {
		t.Fatalf("direct-mode Spawn: %v", err)
	}

	// Both legs land the identical terminal contract.
	for _, leg := range []struct {
		mode   string
		ticket pgtype.UUID
	}{
		{"container", containerTicket},
		{"direct", directTicket},
	} {
		row := m71ReadInstance(t, ctx, pool, leg.ticket)
		if row.Status != "succeeded" || row.ExitReason == nil || *row.ExitReason != ExitCompleted {
			t.Fatalf("%s terminal = (%s, %v); want (succeeded, %s)", leg.mode, row.Status, row.ExitReason, ExitCompleted)
		}
	}

	containerCols := m71RowNullness(t, ctx, pool, containerTicket)
	directCols := m71RowNullness(t, ctx, pool, directTicket)
	if len(containerCols) != len(directCols) {
		t.Fatalf("column counts differ: container=%d direct=%d", len(containerCols), len(directCols))
	}
	for col, containerPopulated := range containerCols {
		directPopulated, ok := directCols[col]
		if !ok {
			t.Errorf("column %q missing from direct-mode row", col)
			continue
		}
		if col == "pid" {
			continue // pinned separately below (plan §5 documented exception)
		}
		if containerPopulated != directPopulated {
			t.Errorf("column %q populated mismatch: container=%v direct=%v", col, containerPopulated, directPopulated)
		}
	}
	if containerCols["pid"] {
		t.Errorf("container-mode pid populated; UpdatePID must be skipped on the container path")
	}
	if !directCols["pid"] {
		t.Errorf("direct-mode pid NULL; the legacy path backfills the subprocess PID")
	}
}

// m71RowNullness maps every agent_instances column to whether it is
// populated (non-NULL) for the instance attached to ticketID.
func m71RowNullness(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ticketID pgtype.UUID) map[string]bool {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT * FROM agent_instances WHERE ticket_id = $1`, ticketID)
	if err != nil {
		t.Fatalf("select agent_instances: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("no agent_instances row for ticket %s", uuidString(ticketID))
	}
	values, err := rows.Values()
	if err != nil {
		t.Fatalf("read row values: %v", err)
	}
	nullness := make(map[string]bool, len(values))
	for i, fd := range rows.FieldDescriptions() {
		nullness[fd.Name] = values[i] != nil
	}
	if rows.Next() {
		t.Fatalf("multiple agent_instances rows for ticket %s", uuidString(ticketID))
	}
	return nullness
}

// writeM71FakeClaude materializes a stand-in claude binary: a shell
// script that ignores its argv and cats the canned NDJSON stream —
// the direct-exec analog of the fake proxy's raw-framed exec stream.
func writeM71FakeClaude(t *testing.T, stream string) string {
	t.Helper()
	dir := t.TempDir()
	streamPath := filepath.Join(dir, "stream.ndjson")
	if err := os.WriteFile(streamPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	scriptPath := filepath.Join(dir, "fake-claude")
	script := "#!/bin/sh\nexec cat " + streamPath + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return scriptPath
}
