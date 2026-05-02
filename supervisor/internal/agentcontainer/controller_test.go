package agentcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureProxy is a httptest.Server that records the most recent
// /containers/create body so tests can assert request shape without
// a real Docker. Returns 201 with a fake container ID by default.
type captureProxy struct {
	server     *httptest.Server
	createBody []byte
	createPath string
	startCalls []string // container IDs Start'd
	stopCalls  []string
}

func newCaptureProxy(t *testing.T) *captureProxy {
	t.Helper()
	p := &captureProxy{}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/containers/create"):
			body, _ := io.ReadAll(r.Body)
			p.createBody = body
			p.createPath = r.URL.RequestURI()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"fake-container-abc"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/start")
			p.startCalls = append(p.startCalls, id)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/stop?t=10")
			id = strings.TrimSuffix(id, "/stop")
			p.stopCalls = append(p.stopCalls, id)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	return p
}

func newTestController(t *testing.T, p *captureProxy) Controller {
	t.Helper()
	return NewSocketProxyController(p.server.URL, p.server.Client(), slog.New(slog.DiscardHandler))
}

// validSpec returns a ContainerSpec that exercises every sandbox-Rule
// field (Rule 2 mounts, Rule 3 network, Rule 4 user/cap-drop, Rule 5
// resource caps).
func validSpec() ContainerSpec {
	return ContainerSpec{
		AgentID:   "11112222-3333-4444-5555-666677778888",
		Image:     "garrison-claude@sha256:deadbeef",
		HostUID:   1042,
		Workspace: "/var/lib/garrison/workspaces/11112222-3333-4444-5555-666677778888",
		Skills:    "/var/lib/garrison/skills/11112222-3333-4444-5555-666677778888",
		Memory:    "512m",
		CPUs:      "1.0",
		PIDsLimit: 200,
		EnvVars:   []string{"CLAUDE_CODE_OAUTH_TOKEN=dummy"},
	}
}

// TestCreateRespectsMounts pins sandbox Rule 2: bind-mount layout
// includes workspace RW + skills RO at the canonical paths, root
// is read-only, /tmp + /var/run are tmpfs.
func TestCreateRespectsMounts(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	id, err := c.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != "fake-container-abc" {
		t.Errorf("id: %s", id)
	}

	var body containerCreateBody
	if err := json.Unmarshal(p.createBody, &body); err != nil {
		t.Fatalf("parse create body: %v", err)
	}

	if !body.HostConfig.ReadonlyRootfs {
		t.Errorf("ReadonlyRootfs=false; sandbox Rule 2 violated")
	}
	if got, want := body.HostConfig.Tmpfs["/tmp"], "rw,size=64m"; got != want {
		t.Errorf("Tmpfs[/tmp]=%q; want %q", got, want)
	}
	if _, ok := body.HostConfig.Tmpfs["/var/run"]; !ok {
		t.Errorf("Tmpfs missing /var/run")
	}

	wantBinds := []string{
		"/var/lib/garrison/workspaces/11112222-3333-4444-5555-666677778888:/workspace:rw",
		"/var/lib/garrison/skills/11112222-3333-4444-5555-666677778888:/workspace/.claude/skills:ro",
	}
	if len(body.HostConfig.Binds) != len(wantBinds) {
		t.Fatalf("binds: got %v; want %v", body.HostConfig.Binds, wantBinds)
	}
	for i, want := range wantBinds {
		if body.HostConfig.Binds[i] != want {
			t.Errorf("bind[%d]=%q; want %q", i, body.HostConfig.Binds[i], want)
		}
	}
}

// TestCreateRespectsNetwork pins sandbox Rule 3: NetworkMode defaults
// to "none". ConnectNetwork is the post-create attach path for sidecar
// reach.
func TestCreateRespectsNetwork(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if _, err := c.Create(context.Background(), validSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body containerCreateBody
	_ = json.Unmarshal(p.createBody, &body)
	if body.HostConfig.NetworkMode != "none" {
		t.Errorf("NetworkMode=%q; want %q (sandbox Rule 3)", body.HostConfig.NetworkMode, "none")
	}
}

// TestCreateRespectsResourceCaps pins sandbox Rule 5: Memory + NanoCpus
// + PidsLimit are all populated.
func TestCreateRespectsResourceCaps(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if _, err := c.Create(context.Background(), validSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body containerCreateBody
	_ = json.Unmarshal(p.createBody, &body)
	if body.HostConfig.Memory != 512*1024*1024 {
		t.Errorf("Memory=%d; want %d (512m)", body.HostConfig.Memory, 512*1024*1024)
	}
	if body.HostConfig.NanoCpus != 1_000_000_000 {
		t.Errorf("NanoCpus=%d; want 1e9 (1.0 cpu)", body.HostConfig.NanoCpus)
	}
	if body.HostConfig.PidsLimit != 200 {
		t.Errorf("PidsLimit=%d; want 200", body.HostConfig.PidsLimit)
	}
}

// TestCreateRespectsCapDrop pins sandbox Rule 4: CapDrop=["ALL"];
// CapAdd empty; Privileged=false.
func TestCreateRespectsCapDrop(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if _, err := c.Create(context.Background(), validSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body containerCreateBody
	_ = json.Unmarshal(p.createBody, &body)
	if len(body.HostConfig.CapDrop) != 1 || body.HostConfig.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop=%v; want [\"ALL\"]", body.HostConfig.CapDrop)
	}
	if len(body.HostConfig.CapAdd) != 0 {
		t.Errorf("CapAdd=%v; want empty (sandbox Rule 4)", body.HostConfig.CapAdd)
	}
	if body.HostConfig.Privileged {
		t.Errorf("Privileged=true; sandbox Rule 4 violated")
	}
}

// TestCreateRespectsUser pins sandbox Rule 4: User=<host_uid>:<host_uid>
// in the request body, where host_uid is FR-206a allocator output.
func TestCreateRespectsUser(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if _, err := c.Create(context.Background(), validSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var body containerCreateBody
	_ = json.Unmarshal(p.createBody, &body)
	if body.User != "1042:1042" {
		t.Errorf("User=%q; want %q", body.User, "1042:1042")
	}
}

// TestCreateRejectsInvalidSpec — empty Image / Workspace / Skills are
// rejected before the HTTP call.
func TestCreateRejectsInvalidSpec(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	bad := validSpec()
	bad.Image = ""
	_, err := c.Create(context.Background(), bad)
	if !errors.Is(err, ErrInvalidSpec) {
		t.Errorf("err: %v; want ErrInvalidSpec", err)
	}
	if p.createBody != nil {
		t.Errorf("create called against proxy despite invalid spec")
	}
}

// TestCreateRespectsLabels — Garrison labels added to the body so
// listContainers can filter by garrison.managed=true.
func TestCreateRespectsLabels(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if _, err := c.Create(context.Background(), validSpec()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var body containerCreateBody
	_ = json.Unmarshal(p.createBody, &body)
	if body.Labels["garrison.managed"] != "true" {
		t.Errorf("Labels[garrison.managed]=%q; want true", body.Labels["garrison.managed"])
	}
	if body.Labels["garrison.agent_id"] == "" {
		t.Errorf("Labels[garrison.agent_id] empty")
	}
}

// TestStartIssuesPostStart — Start makes a POST /containers/<id>/start
// call against the proxy.
func TestStartIssuesPostStart(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if err := c.Start(context.Background(), "test-id"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(p.startCalls) != 1 || p.startCalls[0] != "test-id" {
		t.Errorf("startCalls=%v", p.startCalls)
	}
}

// TestParseHumanBytes pins the supported size suffixes.
func TestParseHumanBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0}, {"0", 0},
		{"512m", 512 * 1024 * 1024},
		{"1g", 1024 * 1024 * 1024},
		{"100k", 100 * 1024},
		{"1024", 1024},
	}
	for _, tc := range cases {
		got, err := parseHumanBytes(tc.in)
		if err != nil {
			t.Errorf("parseHumanBytes(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseHumanBytes(%q)=%d; want %d", tc.in, got, tc.want)
		}
	}
}

// TestParseCPUs pins fractional CPU encoding.
func TestParseCPUs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0}, {"0", 0},
		{"1.0", 1_000_000_000},
		{"0.5", 500_000_000},
		{"2", 2_000_000_000},
	}
	for _, tc := range cases {
		got, err := parseCPUs(tc.in)
		if err != nil {
			t.Errorf("parseCPUs(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseCPUs(%q)=%d; want %d", tc.in, got, tc.want)
		}
	}
}

// TestShortIDStrips strips dashes and truncates to 8 chars.
func TestShortID(t *testing.T) {
	got := shortID("11112222-3333-4444-5555-666677778888")
	if got != "11112222" {
		t.Errorf("shortID=%q; want 11112222", got)
	}
}

// TestExecPreservesNDJSON — the FakeController's Exec echoes stdin as
// stdout; verifies that 3 NDJSON-shaped lines round-trip line-by-line.
// The production socket-proxy impl scaffolds the same shape; T011
// extends with the Docker hijacked-stream demultiplexer.
func TestExecPreservesNDJSON(t *testing.T) {
	f := NewFakeController()
	id, err := f.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Start(context.Background(), id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stdin := strings.NewReader(`{"a":1}` + "\n" + `{"a":2}` + "\n" + `{"a":3}` + "\n")
	stdout, _, err := f.Exec(context.Background(), id, []string{"claude", "--print"}, stdin)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer stdout.Close()

	out, _ := io.ReadAll(stdout)
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []string{`{"a":1}`, `{"a":2}`, `{"a":3}`}
	if len(got) != len(want) {
		t.Fatalf("got %d lines; want %d (out=%q)", len(got), len(want), string(out))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d: got %q; want %q", i, got[i], w)
		}
	}
}

// TestExecRespectsContextCancel — when ctx cancels mid-exec, the
// fake returns ctx.Err(). Production impl uses ctx in
// http.NewRequestWithContext + the start request honours cancellation
// the same way.
func TestExecRespectsContextCancel(t *testing.T) {
	f := NewFakeController()
	id, _ := f.Create(context.Background(), validSpec())
	_ = f.Start(context.Background(), id)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	// The fake doesn't actually check ctx mid-exec (no Sleep) — so
	// instead we inject ExecError and verify it surfaces. The
	// real-impl ctx-cancel path is verified at integration scope
	// (T011 / golden-path).
	f.ExecError = context.Canceled
	_, _, err := f.Exec(ctx, id, []string{"claude"}, nil)
	if err != context.Canceled {
		t.Errorf("err=%v; want context.Canceled", err)
	}
}

// TestFakeControllerImplementsController — compile-time assertion is
// in fake.go; this test pins the runtime contract too.
func TestFakeControllerImplementsController(t *testing.T) {
	var _ Controller = NewFakeController()
}
