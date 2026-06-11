package claudeproto

// CheckMCPHealth iterates an init event's mcp_servers and returns
// (ok=true, "", "") iff every entry has status "connected" or
// "pending". On the first fatal entry it returns (ok=false,
// offenderName, offenderStatus) so the caller can compose the
// exit_reason string (mcp_<offender>_<status>) via
// spawn.FormatMCPFailure.
//
// An empty servers slice is vacuously healthy (ok=true). This is a
// theoretical case in M2.1 — we always configure a postgres server —
// but the function's behaviour is pinned in tests.
//
// Status handling (FR-108 as amended for Claude Code ≥ 2.1.170):
//   - "connected" — healthy.
//   - "pending"   — healthy-but-still-connecting. Claude Code 2.1.117
//     (the M2-spike baseline) emitted the init frame only after MCP
//     connections settled, so FR-108 originally treated any
//     non-"connected" status as fatal. 2.1.170 emits the init frame
//     optimistically while stdio servers are still handshaking — a
//     verified-healthy in-tree finalize server reports "pending" at
//     init and connects moments later. Bailing on "pending" therefore
//     kills every healthy spawn. A server that never finishes
//     connecting still fails closed downstream: its tool calls error
//     and the run exits finalize_never_called / mcp_finalize_pending.
//     Callers should surface pending servers via PendingMCPServers for
//     log visibility.
//   - anything else ("failed", "needs-auth", unknown futures) — fatal,
//     fail-closed, unchanged from the original contract.
func CheckMCPHealth(servers []MCPServer) (ok bool, offenderName, offenderStatus string) {
	for _, s := range servers {
		if s.Status != "connected" && s.Status != "pending" {
			return false, s.Name, s.Status
		}
	}
	return true, "", ""
}

// PendingMCPServers returns the names of servers still handshaking at
// init-frame time (status "pending"). Callers log these so a server
// that later wedges is traceable to its pending-at-init record.
func PendingMCPServers(servers []MCPServer) []string {
	var out []string
	for _, s := range servers {
		if s.Status == "pending" {
			out = append(out, s.Name)
		}
	}
	return out
}
