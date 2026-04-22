package spawn_test

import (
	"context"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
)

// TestSpawnDispatchesOnFakeAgentFlag is the dual-mode routing smoke test
// called for by plan T013: Deps.useFake() must return true whenever the
// explicit UseFakeAgent flag is set OR a non-empty FakeAgentCmd is provided
// (back-compat for M1 callers that predate the explicit flag). Production
// main wires UseFakeAgent directly; existing chaos/integration tests that
// set only FakeAgentCmd continue to take the fake-agent path.
func TestSpawnDispatchesOnFakeAgentFlag(t *testing.T) {
	cases := []struct {
		name     string
		deps     spawn.Deps
		wantFake bool
	}{
		{"explicit UseFakeAgent", spawn.Deps{UseFakeAgent: true}, true},
		{"derived from FakeAgentCmd", spawn.Deps{FakeAgentCmd: `sh -c "echo ok"`}, true},
		{"neither set", spawn.Deps{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.deps.UseFake(); got != tc.wantFake {
				t.Errorf("UseFake() = %v; want %v (deps=%+v)", got, tc.wantFake, tc.deps)
			}
		})
	}
}

// The four classification cases below are the public contract from
// data-model.md §"exit_reason vocabulary". Any change to these strings is a
// breaking change for downstream observers (dashboards, runbooks).

func TestClassifyExitZero(t *testing.T) {
	got := spawn.Classify(0, "", nil, false)
	want := spawn.Classification{Status: "succeeded", ExitReason: "exit_code_0"}
	if got != want {
		t.Errorf("Classify(exit=0) = %+v, want %+v", got, want)
	}
}

func TestClassifyExitNonZero(t *testing.T) {
	got := spawn.Classify(1, "", nil, false)
	want := spawn.Classification{Status: "failed", ExitReason: "exit_code_1"}
	if got != want {
		t.Errorf("Classify(exit=1) = %+v, want %+v", got, want)
	}
}

func TestClassifyTimeout(t *testing.T) {
	// Subprocess-timeout wins over whatever exit code/signal the kernel
	// reports — the operator's mental model is "budget elapsed".
	got := spawn.Classify(-1, "SIGKILL", context.DeadlineExceeded, false)
	want := spawn.Classification{Status: "timeout", ExitReason: "timeout"}
	if got != want {
		t.Errorf("Classify(timeout) = %+v, want %+v", got, want)
	}
}

func TestClassifySIGKILL(t *testing.T) {
	// External SIGKILL (e.g. oom-killer) with no ctx involvement — surface
	// the raw signal name so operators can distinguish it from a timeout.
	got := spawn.Classify(-1, "SIGKILL", nil, false)
	want := spawn.Classification{Status: "failed", ExitReason: "signal_SIGKILL"}
	if got != want {
		t.Errorf("Classify(SIGKILL) = %+v, want %+v", got, want)
	}
}
