package mempalace

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeClientExec records argv + stdin bytes and returns canned stdout.
// Shared across AddDrawer / AddTriples tests.
type fakeClientExec struct {
	stdout, stderr []byte
	err            error

	calls  [][]string
	stdins []string
}

func (f *fakeClientExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdins = append(f.stdins, string(b))
	} else {
		f.stdins = append(f.stdins, "")
	}
	return f.stdout, f.stderr, f.err
}

func (f *fakeClientExec) RunStream(
	ctx context.Context,
	args []string,
	writeStdin func(stdin io.WriteCloser) error,
	scanStdout func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	return nil, errors.New("fakeClientExec: RunStream not implemented")
}

// TestAddDrawerIssuesCorrectJSONRPC — M2.2.1 T003: AddDrawer's stdin
// stream is a valid JSON-RPC sequence: initialize (id=1), tools/call
// mempalace_add_drawer (id=2) with the wing/room/content arguments.
func TestAddDrawerIssuesCorrectJSONRPC(t *testing.T) {
	fake := &fakeClientExec{
		stdout: []byte(
			`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"{\"drawer_id\":\"abc\"}"}]}}` + "\n"),
	}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
	if err := c.AddDrawer(context.Background(), "wing_frontend_engineer", "hall_events", "diary body"); err != nil {
		t.Fatalf("AddDrawer returned err: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls=%d; want 1", len(fake.calls))
	}
	wantArgs := []string{
		"exec", "-i", "garrison-mempalace",
		"python", "-m", "mempalace.mcp_server",
		"--palace", "/palace",
	}
	if len(fake.calls[0]) != len(wantArgs) {
		t.Fatalf("argv=%v; want %v", fake.calls[0], wantArgs)
	}
	for i, w := range wantArgs {
		if fake.calls[0][i] != w {
			t.Errorf("argv[%d]=%q; want %q", i, fake.calls[0][i], w)
		}
	}
	stdin := fake.stdins[0]
	if !strings.Contains(stdin, `"method":"initialize"`) {
		t.Errorf("stdin missing initialize: %s", stdin)
	}
	if !strings.Contains(stdin, `"name":"mempalace_add_drawer"`) {
		t.Errorf("stdin missing add_drawer tool_call: %s", stdin)
	}
	if !strings.Contains(stdin, `"wing":"wing_frontend_engineer"`) {
		t.Errorf("stdin missing wing arg: %s", stdin)
	}
	if !strings.Contains(stdin, `"room":"hall_events"`) {
		t.Errorf("stdin missing room arg: %s", stdin)
	}
	if !strings.Contains(stdin, `"content":"diary body"`) {
		t.Errorf("stdin missing content arg: %s", stdin)
	}
}

// TestAddTriplesIssuesNCalls — M2.2.1 T003: for N triples, stdin carries
// N `tools/call` requests each with `mempalace_kg_add` and the expected
// subject/predicate/object/valid_from shape.
func TestAddTriplesIssuesNCalls(t *testing.T) {
	triples := []Triple{
		{Subject: "agent_instance_1", Predicate: "completed", Object: "ticket_abc", ValidFrom: time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)},
		{Subject: "artifact_foo.go", Predicate: "created_in", Object: "ticket_abc", ValidFrom: time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)},
		{Subject: "decision_x", Predicate: "decided_because", Object: "reason_y", ValidFrom: time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)},
	}
	// Fake response: init (id=1) + 3 kg_add oks (ids 2, 3, 4).
	fake := &fakeClientExec{
		stdout: []byte(
			`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
				`{"jsonrpc":"2.0","id":2,"result":{}}` + "\n" +
				`{"jsonrpc":"2.0","id":3,"result":{}}` + "\n" +
				`{"jsonrpc":"2.0","id":4,"result":{}}` + "\n"),
	}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
	if err := c.AddTriples(context.Background(), triples); err != nil {
		t.Fatalf("AddTriples returned err: %v", err)
	}
	stdin := fake.stdins[0]
	// Count kg_add invocations.
	got := strings.Count(stdin, `"name":"mempalace_kg_add"`)
	if got != 3 {
		t.Errorf("stdin has %d kg_add calls; want 3", got)
	}
	// Spot-check one triple's args.
	if !strings.Contains(stdin, `"subject":"agent_instance_1"`) {
		t.Errorf("stdin missing first triple's subject: %s", stdin)
	}
	if !strings.Contains(stdin, `"predicate":"completed"`) {
		t.Errorf("stdin missing first triple's predicate: %s", stdin)
	}
	if !strings.Contains(stdin, `"object":"ticket_abc"`) {
		t.Errorf("stdin missing first triple's object: %s", stdin)
	}
	if !strings.Contains(stdin, `"valid_from":"2026-04-23T12:00:00Z"`) {
		t.Errorf("stdin missing first triple's valid_from: %s", stdin)
	}
}

// TestAddTriplesNoopOnEmpty — zero-length slice returns nil without
// invoking DockerExec. Documented edge case.
func TestAddTriplesNoopOnEmpty(t *testing.T) {
	fake := &fakeClientExec{}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
	if err := c.AddTriples(context.Background(), nil); err != nil {
		t.Fatalf("AddTriples(nil) returned err: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("expected zero docker exec calls on empty triples; got %d", len(fake.calls))
	}
}

// TestAddDrawerPropagatesExitError — docker exec non-zero exit is
// surfaced as a wrapped ErrQueryFailed whose string includes the
// stderr text for operator debugging.
func TestAddDrawerPropagatesExitError(t *testing.T) {
	fake := &fakeClientExec{
		stdout: []byte(""),
		stderr: []byte("permission denied: /palace"),
		err:    errors.New("exit status 1"),
	}
	c := &Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Exec:               fake,
	}
	err := c.AddDrawer(context.Background(), "wing_x", "hall_events", "body")
	if err == nil {
		t.Fatal("expected error from exec failure")
	}
	if !errors.Is(err, ErrQueryFailed) {
		t.Errorf("expected errors.Is(ErrQueryFailed); got %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should include stderr: %v", err)
	}
}
