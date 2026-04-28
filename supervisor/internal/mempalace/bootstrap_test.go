package mempalace

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeExec records the argv it was called with and returns canned stdout,
// stderr, and error. runFn, if set, overrides this and returns dynamic
// results (used by timeout tests to block).
type fakeExec struct {
	stdout, stderr []byte
	err            error
	calls          [][]string

	runFn func(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error)
}

func (f *fakeExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.runFn != nil {
		return f.runFn(ctx, args, stdin)
	}
	return f.stdout, f.stderr, f.err
}

// RunStream is unused by the mempalace one-shot call sites; the fake
// is satisfied as DockerExec only via this stub. Tests that exercise
// stream-shaped exec live under internal/dockerexec or internal/chat.
func (f *fakeExec) RunStream(
	ctx context.Context,
	args []string,
	writeStdin func(stdin io.WriteCloser) error,
	scanStdout func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	return nil, errors.New("fakeExec: RunStream not implemented")
}

func TestBootstrapRunsInit(t *testing.T) {
	fake := &fakeExec{stdout: []byte("Config saved: /palace/mempalace.yaml\n")}
	cfg := BootstrapConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	if err := Bootstrap(context.Background(), cfg); err != nil {
		t.Fatalf("Bootstrap returned err: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 docker-exec call, got %d", len(fake.calls))
	}
	got := fake.calls[0]
	want := []string{"exec", "garrison-mempalace", "mempalace", "init", "--yes", "/palace"}
	if !sliceEq(got, want) {
		t.Fatalf("unexpected argv\n  got:  %v\n  want: %v", got, want)
	}
}

func TestBootstrapIdempotent(t *testing.T) {
	// T001 finding F1: mempalace init --yes is idempotent in 3.3.2. A
	// second call against an initialized palace should be a no-op at the
	// supervisor level; Bootstrap just invokes init again, receives exit 0,
	// and returns nil. No internal state flip, no behaviour change.
	fake := &fakeExec{stdout: []byte("Config saved: /palace/mempalace.yaml\n")}
	cfg := BootstrapConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	for i := 0; i < 2; i++ {
		if err := Bootstrap(context.Background(), cfg); err != nil {
			t.Fatalf("call %d returned err: %v", i+1, err)
		}
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 docker-exec calls, got %d", len(fake.calls))
	}
	// Both calls must be byte-identical.
	if !sliceEq(fake.calls[0], fake.calls[1]) {
		t.Fatalf("calls differ:\n  0: %v\n  1: %v", fake.calls[0], fake.calls[1])
	}
}

func TestBootstrapFailsOnInitError(t *testing.T) {
	fake := &fakeExec{
		stderr: []byte("EOFError: EOF when reading a line\n"),
		err:    &exec.ExitError{}, // simulates non-zero exit
	}
	cfg := BootstrapConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	err := Bootstrap(context.Background(), cfg)
	if err == nil {
		t.Fatal("Bootstrap returned nil; expected wrapped ErrPalaceInitFailed")
	}
	if !errors.Is(err, ErrPalaceInitFailed) {
		t.Fatalf("error does not wrap ErrPalaceInitFailed: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "/palace") {
		t.Errorf("error should name the palace path; got: %s", msg)
	}
	if !strings.Contains(msg, "EOFError") {
		t.Errorf("error should carry the stderr snippet; got: %s", msg)
	}
}

// Quick tests of guard-rails. Not part of the T006 nine-test list but
// cheap and worth pinning.
func TestBootstrapRejectsEmptyContainer(t *testing.T) {
	err := Bootstrap(context.Background(), BootstrapConfig{PalacePath: "/palace"})
	if err == nil {
		t.Fatal("expected error on empty container")
	}
}
func TestBootstrapRejectsEmptyPalacePath(t *testing.T) {
	err := Bootstrap(context.Background(), BootstrapConfig{MempalaceContainer: "x"})
	if err == nil {
		t.Fatal("expected error on empty palace path")
	}
}

// sliceEq is an unordered small-string slice equality check. We could use
// reflect.DeepEqual but a hand-rolled helper keeps stdlib-only deps clean
// and avoids allocating a reflect.Value per call.
func sliceEq(a, b []string) bool {
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

// Shared deadline helper for the wake-up tests' timeout-case.
func mustDeadline(t *testing.T, ctx context.Context) time.Time {
	t.Helper()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected ctx to carry a deadline")
	}
	return dl
}
