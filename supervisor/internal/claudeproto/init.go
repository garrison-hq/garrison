package claudeproto

// CheckMCPHealth iterates an init event's mcp_servers and returns
// (ok=true, "", "") iff every entry has status "connected". On the first
// non-"connected" entry it returns (ok=false, offenderName,
// offenderStatus) so the caller can compose the exit_reason string
// (mcp_<offender>_<status>) via spawn.FormatMCPFailure.
//
// An empty servers slice is vacuously healthy (ok=true). This is a
// theoretical case in M2.1 — we always configure a postgres server —
// but the function's behaviour is pinned in tests.
//
// Status comparison is exact against the literal "connected". Per FR-108
// any other value (including the observed-at-spike values "failed" /
// "needs-auth" and any future unknown string) is treated as failure
// (fail-closed). This is the init-event-parsing contract ARCHITECTURE
// and m2.1-context.md commit to.
func CheckMCPHealth(servers []MCPServer) (ok bool, offenderName, offenderStatus string) {
	for _, s := range servers {
		if s.Status != "connected" {
			return false, s.Name, s.Status
		}
	}
	return true, "", ""
}
