package claudeproto

import "testing"

// TestCheckMCPHealthAllConnected pins the happy-path: every server
// status=="connected" yields ok=true and empty offender fields.
func TestCheckMCPHealthAllConnected(t *testing.T) {
	servers := []MCPServer{
		{Name: "postgres", Status: "connected"},
		{Name: "mempalace", Status: "connected"},
	}
	ok, name, status := CheckMCPHealth(servers)
	if !ok {
		t.Fatalf("expected ok=true, got false (offender %q/%q)", name, status)
	}
	if name != "" || status != "" {
		t.Errorf("expected empty offender fields, got %q/%q", name, status)
	}
}

// TestCheckMCPHealthOneFailed pins FR-108: any non-"connected" entry
// trips the check and reports the first-seen offender.
func TestCheckMCPHealthOneFailed(t *testing.T) {
	servers := []MCPServer{
		{Name: "postgres", Status: "connected"},
		{Name: "badserver", Status: "failed"},
	}
	ok, name, status := CheckMCPHealth(servers)
	if ok {
		t.Fatalf("expected ok=false")
	}
	if name != "badserver" || status != "failed" {
		t.Errorf("offender: got %q/%q, want badserver/failed", name, status)
	}
}

// TestCheckMCPHealthNeedsAuth pins the "needs-auth" status (observed for
// pre-authorized MCPs in the spike).
func TestCheckMCPHealthNeedsAuth(t *testing.T) {
	servers := []MCPServer{{Name: "gmail", Status: "needs-auth"}}
	ok, name, status := CheckMCPHealth(servers)
	if ok {
		t.Fatalf("expected ok=false for needs-auth")
	}
	if name != "gmail" || status != "needs-auth" {
		t.Errorf("offender: got %q/%q", name, status)
	}
}

// TestCheckMCPHealthUnknownStatus pins the fail-closed behaviour for a
// status value outside the observed {connected, failed, needs-auth} set
// (FR-108, spec clarify Q3-adjacent). Future Claude versions may add
// status strings; the supervisor must reject anything but "connected".
func TestCheckMCPHealthUnknownStatus(t *testing.T) {
	servers := []MCPServer{{Name: "postgres", Status: "quantum-superposition"}}
	ok, name, status := CheckMCPHealth(servers)
	if ok {
		t.Fatalf("expected ok=false for unknown status")
	}
	if name != "postgres" || status != "quantum-superposition" {
		t.Errorf("offender: got %q/%q", name, status)
	}
}

// TestCheckMCPHealthEmptyServers pins the vacuous-true behaviour: an
// empty mcp_servers array reports healthy. M2.1 always configures at
// least one server, so this is a theoretical case; pinning it prevents
// a future refactor from accidentally flipping to fail-closed on empty.
func TestCheckMCPHealthEmptyServers(t *testing.T) {
	ok, name, status := CheckMCPHealth(nil)
	if !ok {
		t.Fatalf("expected ok=true for empty servers")
	}
	if name != "" || status != "" {
		t.Errorf("expected empty offender, got %q/%q", name, status)
	}
	ok2, _, _ := CheckMCPHealth([]MCPServer{})
	if !ok2 {
		t.Fatalf("expected ok=true for zero-length slice")
	}
}
