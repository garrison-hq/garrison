package mcpserverwork

import "testing"

func TestValidTerminalTransitionRejectsNonPending(t *testing.T) {
	cases := []struct {
		from, to State
		want     bool
	}{
		{StatePending, StateRegistered, true},
		{StatePending, StateFailed, true},
		{StatePending, StateDeregistered, false},
		{StateRegistered, StateFailed, false},
		{StateFailed, StateRegistered, false},
		{StatePending, StatePending, false},
	}
	for _, c := range cases {
		got := ValidTerminalTransition(c.from, c.to)
		if got != c.want {
			t.Errorf("ValidTerminalTransition(%s,%s) = %v; want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestStateConstants(t *testing.T) {
	if StatePending != "pending" {
		t.Errorf("StatePending = %q", StatePending)
	}
	if StateRegistered != "registered" {
		t.Errorf("StateRegistered = %q", StateRegistered)
	}
	if StateFailed != "failed" {
		t.Errorf("StateFailed = %q", StateFailed)
	}
	if StateDeregistered != "deregistered" {
		t.Errorf("StateDeregistered = %q", StateDeregistered)
	}
}
