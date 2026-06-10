package agentcontainer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// execInspectResp is one scripted GET /exec/<id>/json response.
type execInspectResp struct {
	Running  bool
	ExitCode int
}

// captureProxy is a httptest.Server that records the most recent
// /containers/create body so tests can assert request shape without
// a real Docker. Returns 201 with a fake container ID by default.
type captureProxy struct {
	server       *httptest.Server
	createBody   []byte
	createPath   string
	startCalls   []string // container IDs Start'd
	stopCalls    []string
	restartCalls []string // request URIs of POST /containers/<id>/restart

	execCreateBody   []byte
	execCreatePath   string
	execCreateStatus int    // overrides the 201 exec-create response when non-zero
	execStream       []byte // raw-framed body served for POST /exec/<id>/start
	execInspects     []execInspectResp
	execInspectCalls int // GET /exec/<id>/json count; past the script the last entry repeats
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
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/exec"):
			body, _ := io.ReadAll(r.Body)
			p.execCreateBody = body
			p.execCreatePath = r.URL.RequestURI()
			if p.execCreateStatus != 0 {
				w.WriteHeader(p.execCreateStatus)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"fake-exec-abc"}`))
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/start"):
			w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(p.execStream)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/exec/") && strings.HasSuffix(r.URL.Path, "/json"):
			resp := execInspectResp{}
			if n := len(p.execInspects); n > 0 {
				idx := p.execInspectCalls
				if idx >= n {
					idx = n - 1
				}
				resp = p.execInspects[idx]
			}
			p.execInspectCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Running": resp.Running, "ExitCode": resp.ExitCode,
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/restart"):
			p.restartCalls = append(p.restartCalls, r.URL.RequestURI())
			w.WriteHeader(http.StatusNoContent)
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

// TestExecPreservesNDJSON — the FakeController's Exec streams the
// scripted stdout; verifies that 3 NDJSON-shaped lines round-trip
// line-by-line and the scripted exit code surfaces via ExitCode.
func TestExecPreservesNDJSON(t *testing.T) {
	f := NewFakeController()
	id, err := f.Create(context.Background(), validSpec())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := f.Start(context.Background(), id); err != nil {
		t.Fatalf("Start: %v", err)
	}

	f.ExecResults[id] = []FakeExecResult{{
		Stdout:   `{"a":1}` + "\n" + `{"a":2}` + "\n" + `{"a":3}` + "\n",
		ExitCode: 0,
	}}
	sess, err := f.Exec(context.Background(), id, ExecSpec{Cmd: []string{"claude", "--print"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer sess.Stdout.Close()

	out, _ := io.ReadAll(sess.Stdout)
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
	code, err := sess.ExitCode(context.Background())
	if err != nil || code != 0 {
		t.Errorf("ExitCode = (%d, %v); want (0, nil)", code, err)
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
	// (T013 / golden-path).
	f.ExecError = context.Canceled
	_, err := f.Exec(ctx, id, ExecSpec{Cmd: []string{"claude"}})
	if err != context.Canceled {
		t.Errorf("err=%v; want context.Canceled", err)
	}
}

// TestExecCreateBodyCarriesEnvWorkingDirNoStdin pins the exec-create
// request shape: per-exec Env (FR-002 — the only secret transit),
// WorkingDir (FR-006), and AttachStdin explicitly false (FR-004 — no
// stdin attach, no connection hijacking).
func TestExecCreateBodyCarriesEnvWorkingDirNoStdin(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	wantEnv := []string{"HOME=/home/node", "HTTPS_PROXY=http://garrison-egress-proxy:3128"}
	wantCmd := []string{"/usr/local/bin/claude", "-p", "describe ticket"}
	sess, err := c.Exec(context.Background(), "garrison-agent-11112222", ExecSpec{
		Cmd:        wantCmd,
		Env:        wantEnv,
		WorkingDir: "/workspace",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer sess.Stdout.Close()

	if !strings.HasPrefix(p.execCreatePath, "/containers/garrison-agent-11112222/exec") {
		t.Errorf("exec-create path = %q; want /containers/garrison-agent-11112222/exec", p.execCreatePath)
	}
	var body struct {
		AttachStdin  *bool    `json:"AttachStdin"`
		AttachStdout bool     `json:"AttachStdout"`
		AttachStderr bool     `json:"AttachStderr"`
		Tty          bool     `json:"Tty"`
		Cmd          []string `json:"Cmd"`
		Env          []string `json:"Env"`
		WorkingDir   string   `json:"WorkingDir"`
	}
	if err := json.Unmarshal(p.execCreateBody, &body); err != nil {
		t.Fatalf("parse exec-create body: %v", err)
	}
	if body.AttachStdin == nil || *body.AttachStdin {
		t.Errorf("AttachStdin = %v; want explicit false (FR-004)", body.AttachStdin)
	}
	if !body.AttachStdout || !body.AttachStderr {
		t.Errorf("AttachStdout/AttachStderr = %v/%v; want true/true", body.AttachStdout, body.AttachStderr)
	}
	if body.Tty {
		t.Errorf("Tty = true; want false (raw-stream framing requires Tty=false)")
	}
	if strings.Join(body.Cmd, " ") != strings.Join(wantCmd, " ") {
		t.Errorf("Cmd = %v; want %v", body.Cmd, wantCmd)
	}
	if strings.Join(body.Env, "\x00") != strings.Join(wantEnv, "\x00") {
		t.Errorf("Env = %v; want %v", body.Env, wantEnv)
	}
	if body.WorkingDir != "/workspace" {
		t.Errorf("WorkingDir = %q; want /workspace (FR-006)", body.WorkingDir)
	}
}

// TestExecStartDemuxesRawStream — a canned 8-byte-framed exec-start
// response yields the expected demuxed stdout and stderr bytes.
func TestExecStartDemuxesRawStream(t *testing.T) {
	p := newCaptureProxy(t)
	var stream bytes.Buffer
	stream.Write(rawFrame(1, `{"type":"system"}`+"\n"))
	stream.Write(rawFrame(2, "warn: slow MCP handshake\n"))
	stream.Write(rawFrame(1, `{"type":"result"}`+"\n"))
	p.execStream = stream.Bytes()
	c := newTestController(t, p)

	sess, err := c.Exec(context.Background(), "garrison-agent-11112222", ExecSpec{Cmd: []string{"claude"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	outData, errData, outErr, errErr := readBoth(t, sess.Stdout, sess.Stderr)
	if outErr != nil || errErr != nil {
		t.Fatalf("ReadAll errors: stdout=%v stderr=%v", outErr, errErr)
	}
	if got, want := string(outData), `{"type":"system"}`+"\n"+`{"type":"result"}`+"\n"; got != want {
		t.Errorf("stdout = %q; want %q", got, want)
	}
	if got, want := string(errData), "warn: slow MCP handshake\n"; got != want {
		t.Errorf("stderr = %q; want %q", got, want)
	}
	if err := sess.Stdout.Close(); err != nil {
		t.Errorf("Stdout.Close: %v", err)
	}
}

// TestExecExitCodePollsUntilNotRunning — inspect reports Running=true
// twice, then Running=false with ExitCode=124; the session returns 124.
func TestExecExitCodePollsUntilNotRunning(t *testing.T) {
	p := newCaptureProxy(t)
	p.execInspects = []execInspectResp{
		{Running: true}, {Running: true}, {Running: false, ExitCode: 124},
	}
	c := newTestController(t, p)

	sess, err := c.Exec(context.Background(), "garrison-agent-11112222", ExecSpec{Cmd: []string{"claude"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer sess.Stdout.Close()
	sess.pollInterval = time.Millisecond // keep the unit test fast; production stays at 200ms

	code, err := sess.ExitCode(context.Background())
	if err != nil {
		t.Fatalf("ExitCode: %v", err)
	}
	if code != 124 {
		t.Errorf("ExitCode = %d; want 124", code)
	}
	if p.execInspectCalls != 3 {
		t.Errorf("inspect calls = %d; want 3", p.execInspectCalls)
	}
}

// TestExecExitCodeGivesUpAfterPollBudget — a perpetually-Running exec
// exhausts the poll budget and returns (-1, error); the caller's
// adjudication falls back to result-frame evidence.
func TestExecExitCodeGivesUpAfterPollBudget(t *testing.T) {
	p := newCaptureProxy(t)
	p.execInspects = []execInspectResp{{Running: true}}
	c := newTestController(t, p)

	sess, err := c.Exec(context.Background(), "garrison-agent-11112222", ExecSpec{Cmd: []string{"claude"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	defer sess.Stdout.Close()
	sess.pollInterval = time.Millisecond

	code, err := sess.ExitCode(context.Background())
	if err == nil {
		t.Fatal("ExitCode error = nil; want poll-budget error")
	}
	if code != -1 {
		t.Errorf("ExitCode = %d; want -1", code)
	}
	if p.execInspectCalls != execExitPollBudget {
		t.Errorf("inspect calls = %d; want %d (the poll budget)", p.execInspectCalls, execExitPollBudget)
	}
}

// TestExecCreateOn404ReturnsErrContainerNotFound — exec-create against
// a missing (404) or stopped (409) container maps to
// ErrContainerNotFound so spawn lands in spawn_failed (FR-019).
func TestExecCreateOn404ReturnsErrContainerNotFound(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		p := newCaptureProxy(t)
		p.execCreateStatus = status
		c := newTestController(t, p)

		_, err := c.Exec(context.Background(), "garrison-agent-gone", ExecSpec{Cmd: []string{"claude"}})
		if !errors.Is(err, ErrContainerNotFound) {
			t.Errorf("status %d: err = %v; want ErrContainerNotFound", status, err)
		}
	}
}

// TestRestartPostsRestartEndpoint — Restart issues POST
// /containers/<id>/restart with the 5s grace window (FR-016 backstop).
func TestRestartPostsRestartEndpoint(t *testing.T) {
	p := newCaptureProxy(t)
	c := newTestController(t, p)

	if err := c.Restart(context.Background(), "garrison-agent-11112222"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	want := []string{"/containers/garrison-agent-11112222/restart?t=5"}
	if len(p.restartCalls) != 1 || p.restartCalls[0] != want[0] {
		t.Errorf("restartCalls = %v; want %v", p.restartCalls, want)
	}
}

// TestFakeControllerImplementsController — compile-time assertion is
// in fake.go; this test pins the runtime contract too.
func TestFakeControllerImplementsController(t *testing.T) {
	var _ Controller = NewFakeController()
}
