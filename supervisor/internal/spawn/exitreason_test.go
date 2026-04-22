package spawn

import (
	"syscall"
	"testing"
)

// TestFormatMCPFailureCanonical pins the canonical shape FR-108 emits into
// agent_instances.exit_reason when an init-event MCP health check fails.
func TestFormatMCPFailureCanonical(t *testing.T) {
	cases := []struct {
		name, status, want string
	}{
		{"postgres", "failed", "mcp_postgres_failed"},
		{"mempalace", "needs-auth", "mcp_mempalace_needs-auth"},
		{"postgres", "connected", "mcp_postgres_connected"}, // not a failure in practice, but the formatter is content-agnostic
	}
	for _, c := range cases {
		if got := FormatMCPFailure(c.name, c.status); got != c.want {
			t.Errorf("FormatMCPFailure(%q, %q) = %q, want %q", c.name, c.status, got, c.want)
		}
	}
}

// TestFormatMCPFailureHonoursUnknownStatus verifies fail-closed behaviour
// from FR-108: a status value outside the observed {connected, failed,
// needs-auth} enum is passed through verbatim so operators can diagnose.
func TestFormatMCPFailureHonoursUnknownStatus(t *testing.T) {
	got := FormatMCPFailure("postgres", "quantum-superposition")
	want := "mcp_postgres_quantum-superposition"
	if got != want {
		t.Errorf("FormatMCPFailure pass-through: got %q, want %q", got, want)
	}
}

// TestFormatMCPFailureRejectsEmpty verifies defensive handling of a
// supervisor-side bug where the server name is empty. Claude's init event
// always carries a name, so empty is an internal-error signal.
func TestFormatMCPFailureRejectsEmpty(t *testing.T) {
	got := FormatMCPFailure("", "failed")
	want := "mcp_unknown_failed"
	if got != want {
		t.Errorf("FormatMCPFailure empty server: got %q, want %q", got, want)
	}
}

// TestFormatSignalledKnownSignals pins the "signaled_SIG…" format for the
// four signals the supervisor realistically observes externally on Linux.
func TestFormatSignalledKnownSignals(t *testing.T) {
	cases := []struct {
		sig  syscall.Signal
		want string
	}{
		{syscall.SIGKILL, "signaled_SIGKILL"},
		{syscall.SIGTERM, "signaled_SIGTERM"},
		{syscall.SIGINT, "signaled_SIGINT"},
		{syscall.SIGHUP, "signaled_SIGHUP"},
	}
	for _, c := range cases {
		if got := FormatSignalled(c.sig); got != c.want {
			t.Errorf("FormatSignalled(%v) = %q, want %q", c.sig, got, c.want)
		}
	}
}

// TestFormatSignalledUnknownSignalFallsBackToNumeric protects against a
// future Linux kernel introducing a signal we have not added to the switch;
// fallback encodes the numeric value so the value is at least debuggable.
func TestFormatSignalledUnknownSignalFallsBackToNumeric(t *testing.T) {
	// Signal 99 is unused on all common Linux kernels; if a future kernel
	// assigns it a name, this test will remind us to add a case above.
	got := FormatSignalled(syscall.Signal(99))
	want := "signaled_signal_99"
	if got != want {
		t.Errorf("FormatSignalled(99) = %q, want %q", got, want)
	}
}
