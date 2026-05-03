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

	"github.com/garrison-hq/garrison/supervisor/internal/agentpolicy"
)

// agentpolicyBodyForTest exposes the package-private import via a tiny
// local indirection so the imports above stay tidy.
func agentpolicyBodyForTest() string {
	// Take a 64-char prefix of the preamble body so the test substring
	// is stable but doesn't depend on exact full-body bytes — the byte-
	// equality test in agentpolicy already pins those.
	body := agentpolicy.Body()
	if len(body) > 64 {
		return body[:64]
	}
	return body
}

func TestWakeupOK(t *testing.T) {
	fake := &fakeExec{stdout: []byte("Wake-up text (~79 tokens):\n##L0...")}
	cfg := WakeupConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	stdout, status, elapsed, err := Wakeup(context.Background(), cfg, "wing_frontend_engineer")
	if err != nil {
		t.Fatalf("Wakeup returned err: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status=%s; want %s", status, StatusOK)
	}
	if stdout == "" {
		t.Fatal("expected non-empty stdout")
	}
	if elapsed < 0 {
		t.Fatalf("elapsed=%s; want ≥0", elapsed)
	}
	// Verify argv shape per T001 finding F2: --palace is top-level, wake-up
	// subcommand takes only --wing, NO --max-tokens.
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call; got %d", len(fake.calls))
	}
	got := fake.calls[0]
	want := []string{
		"exec", "garrison-mempalace",
		"mempalace", "--palace", "/palace",
		"wake-up", "--wing", "wing_frontend_engineer",
	}
	if !sliceEq(got, want) {
		t.Fatalf("unexpected argv\n  got:  %v\n  want: %v", got, want)
	}
	// Explicitly confirm --max-tokens is absent.
	for _, a := range got {
		if a == "--max-tokens" {
			t.Fatalf("--max-tokens present in argv; T001 finding F2 says the flag doesn't exist: %v", got)
		}
	}
}

func TestWakeupTimeout(t *testing.T) {
	fake := &fakeExec{
		runFn: func(ctx context.Context, args []string, _ io.Reader) ([]byte, []byte, error) {
			<-ctx.Done()
			return nil, nil, context.DeadlineExceeded
		},
	}
	cfg := WakeupConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            50 * time.Millisecond,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	stdout, status, elapsed, err := Wakeup(context.Background(), cfg, "wing_x")
	if err != nil {
		t.Fatalf("Wakeup returned err: %v; expected nil (non-blocking)", err)
	}
	if status != StatusFailed {
		t.Fatalf("status=%s; want %s", status, StatusFailed)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout on failure; got %q", stdout)
	}
	if elapsed < 40*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed=%s; want near the 50ms timeout", elapsed)
	}
}

func TestWakeupNonZeroExit(t *testing.T) {
	fake := &fakeExec{
		stderr: []byte("error: invalid wing\n"),
		err:    &exec.ExitError{},
	}
	cfg := WakeupConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	stdout, status, _, err := Wakeup(context.Background(), cfg, "wing_x")
	if err != nil {
		t.Fatalf("Wakeup returned err: %v; expected nil (non-blocking)", err)
	}
	if status != StatusFailed {
		t.Fatalf("status=%s; want %s", status, StatusFailed)
	}
	if stdout != "" {
		t.Fatalf("expected empty stdout; got %q", stdout)
	}
}

func TestWakeupExecError(t *testing.T) {
	// Simulates docker binary not found. Not an ExitError — an exec error
	// wrapping the underlying OS lookup failure.
	fake := &fakeExec{err: errors.New(`exec: "docker": executable file not found in $PATH`)}
	cfg := WakeupConfig{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            2 * time.Second,
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Exec:               fake,
	}
	_, status, _, err := Wakeup(context.Background(), cfg, "wing_x")
	if err != nil {
		t.Fatalf("Wakeup returned err: %v; expected nil (non-blocking)", err)
	}
	if status != StatusFailed {
		t.Fatalf("status=%s; want %s", status, StatusFailed)
	}
}

func TestComposeSystemPromptWithWakeUp(t *testing.T) {
	got := ComposeSystemPrompt("AGENT_MD_CONTENT", "WAKE_UP_STDOUT", "TKT-1", "INST-1")
	if !strings.Contains(got, "AGENT_MD_CONTENT") {
		t.Error("missing agent.md content")
	}
	if !strings.Contains(got, "## Wake-up context") {
		t.Error("missing Wake-up context heading")
	}
	if !strings.Contains(got, "WAKE_UP_STDOUT") {
		t.Error("missing wake-up stdout")
	}
	if !strings.Contains(got, "## This turn") {
		t.Error("missing This turn heading")
	}
	if !strings.Contains(got, "agent_instance INST-1") {
		t.Error("missing instance_id substitution")
	}
	if !strings.Contains(got, "ticket TKT-1") {
		t.Error("missing ticket_id substitution")
	}
}

func TestComposeSystemPromptWithoutWakeUp(t *testing.T) {
	// wake-up stdout is empty → Wake-up context block is omitted entirely.
	got := ComposeSystemPrompt("AGENT_MD_CONTENT", "", "TKT-1", "INST-1")
	if strings.Contains(got, "## Wake-up context") {
		t.Error("Wake-up context block present; should be omitted when stdout is empty")
	}
	if !strings.Contains(got, "AGENT_MD_CONTENT") {
		t.Error("missing agent.md content")
	}
	if !strings.Contains(got, "## This turn") {
		t.Error("missing This turn block")
	}
	if !strings.Contains(got, "agent_instance INST-1") {
		t.Error("missing instance_id substitution")
	}
	if !strings.Contains(got, "ticket TKT-1") {
		t.Error("missing ticket_id substitution")
	}
}

// TestComposeSystemPromptPrependsPreamble (M7 FR-303 / T011) pins the
// preamble's prompt-position above agent.md. The first agentpolicy.Body
// snippet appears before the agent.md substring on every spawn,
// regardless of whether the wake-up succeeded.
func TestComposeSystemPromptPrependsPreamble(t *testing.T) {
	pre := agentpolicyBodyForTest()

	cases := []struct {
		name string
		out  string
	}{
		{"with wake-up", ComposeSystemPrompt("AGENT_MD_CONTENT", "WAKE_UP_STDOUT", "TKT-1", "INST-1")},
		{"empty wake-up", ComposeSystemPrompt("AGENT_MD_CONTENT", "", "TKT-1", "INST-1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			preIdx := strings.Index(c.out, pre)
			if preIdx == -1 {
				t.Fatalf("preamble body absent from system prompt")
			}
			agentIdx := strings.Index(c.out, "AGENT_MD_CONTENT")
			if agentIdx == -1 {
				t.Fatalf("agent.md content absent")
			}
			if preIdx >= agentIdx {
				t.Errorf("preamble must precede agent.md; preIdx=%d agentIdx=%d", preIdx, agentIdx)
			}
		})
	}
}
