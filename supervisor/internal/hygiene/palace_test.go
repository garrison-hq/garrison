package hygiene

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"
)

// fakePalaceExec records argv + stdin bytes and returns canned stdout.
// Separate from mempalace's fakeExec because it needs to capture the
// stdin bytes the client sent (for JSON-RPC request verification).
type fakePalaceExec struct {
	stdout, stderr []byte
	err            error

	calls   [][]string
	stdins  []string
	runFn   func(ctx context.Context) ([]byte, []byte, error)
}

func (f *fakePalaceExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdins = append(f.stdins, string(b))
	} else {
		f.stdins = append(f.stdins, "")
	}
	if f.runFn != nil {
		return f.runFn(ctx)
	}
	return f.stdout, f.stderr, f.err
}

// mcpResponseLine is a convenience for fixture construction.
const (
	initRespLine    = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{}}}` + "\n"
	searchRespLine  = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"results\":[{\"wing\":\"wing_frontend_engineer\",\"content\":\"This covers ticket_abc-123 at length well over 100 characters with the engineer's findings and rationale for the approach taken.\",\"created_at\":\"2026-04-23T12:01:00Z\"}]}"}]}}` + "\n"
	kgRespLine      = `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"{\"triples\":[{\"subject\":\"agent_instance_xyz\",\"predicate\":\"completed\",\"object\":\"ticket_abc-123\",\"valid_from\":\"2026-04-23T12:01:00Z\"}]}"}]}}` + "\n"
)

func TestClientQuerySuccess(t *testing.T) {
	fake := &fakePalaceExec{
		stdout: []byte(initRespLine + searchRespLine + kgRespLine),
	}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
	drawers, triples, err := c.Query(context.Background(), "ticket_abc-123", "wing_frontend_engineer", TimeWindow{})
	if err != nil {
		t.Fatalf("Query returned err: %v", err)
	}
	if len(drawers) != 1 {
		t.Fatalf("drawers=%d; want 1", len(drawers))
	}
	if drawers[0].Wing != "wing_frontend_engineer" {
		t.Errorf("wing=%q", drawers[0].Wing)
	}
	if len(triples) != 1 {
		t.Fatalf("triples=%d; want 1", len(triples))
	}
	if triples[0].Object != "ticket_abc-123" {
		t.Errorf("triple.object=%q", triples[0].Object)
	}
	// argv shape
	if len(fake.calls) != 1 {
		t.Fatalf("calls=%d; want 1", len(fake.calls))
	}
	wantArgs := []string{
		"exec", "-i", "garrison-mempalace",
		"python", "-m", "mempalace.mcp_server",
		"--palace", "/palace",
	}
	got := fake.calls[0]
	if len(got) != len(wantArgs) {
		t.Fatalf("argv len=%d; want %d", len(got), len(wantArgs))
	}
	for i := range got {
		if got[i] != wantArgs[i] {
			t.Errorf("argv[%d]=%q; want %q", i, got[i], wantArgs[i])
		}
	}
	// stdin must contain 3 JSON-RPC requests, one per line.
	if n := countLines(fake.stdins[0]); n != 3 {
		t.Errorf("stdin has %d lines; want 3 (initialize, search, kg_query)", n)
	}
}

func TestClientQueryTimeout(t *testing.T) {
	fake := &fakePalaceExec{
		runFn: func(ctx context.Context) ([]byte, []byte, error) {
			<-ctx.Done()
			return nil, nil, context.DeadlineExceeded
		},
	}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            30 * time.Millisecond,
		Exec:               fake,
	}
	_, _, err := c.Query(context.Background(), "ticket_abc-123", "wing_x", TimeWindow{})
	if err == nil {
		t.Fatal("expected err on timeout")
	}
	if !errors.Is(err, ErrPalaceQueryFailed) {
		t.Fatalf("err does not wrap ErrPalaceQueryFailed: %v", err)
	}
}

func TestClientQueryDockerError(t *testing.T) {
	fake := &fakePalaceExec{
		stderr: []byte("Error response from daemon: No such container: garrison-mempalace\n"),
		err:    &exec.ExitError{},
	}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
	_, _, err := c.Query(context.Background(), "ticket_abc-123", "wing_x", TimeWindow{})
	if err == nil {
		t.Fatal("expected err on docker-exit error")
	}
	if !errors.Is(err, ErrPalaceQueryFailed) {
		t.Fatalf("err does not wrap ErrPalaceQueryFailed: %v", err)
	}
	if !contains(err.Error(), "No such container") {
		t.Errorf("err message should carry stderr snippet: %v", err)
	}
}

func countLines(s string) int {
	n := 0
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}

func contains(haystack, needle string) bool {
	return indexOf(haystack, needle) >= 0
}
