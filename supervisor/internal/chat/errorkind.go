package chat

import "fmt"

// ErrorKind is the typed-string vocabulary written to
// chat_messages.error_kind on terminal write paths that aren't a
// successful result. Every value is exact-match snake_case; adding a
// new error_kind requires explicit code change here AND in
// errorkind_test.go's vocabulary table (compile-time + test-time
// safety net mirroring dashboard/lib/vault/outcomes.test.ts).
//
// Categories (FR-002 / FR-003 / FR-031 / FR-061 / FR-081 / FR-083 /
// FR-016 / spec edge cases):
//
//   Vault path
//     ErrorTokenNotFound, ErrorTokenExpired, ErrorVaultUnavailable
//   MCP-health path (constructed via BuildMCPErrorKind)
//     "mcp_<server>_<status>" — e.g. mcp_postgres_failed
//   Spawn / runtime
//     ErrorContainerCrashed, ErrorDockerProxyUnreachable,
//     ErrorRateLimitExhausted, ErrorClaudeRuntimeError,
//     ErrorTurnTimeout
//   Lifecycle / quota
//     ErrorSessionCostCapReached, ErrorSessionEnded,
//     ErrorSessionNotFound
//   Shutdown / restart
//     ErrorSupervisorShutdown, ErrorSupervisorRestart
type ErrorKind = string

const (
	// Vault path
	ErrorTokenNotFound    ErrorKind = "token_not_found"
	ErrorTokenExpired     ErrorKind = "token_expired"
	ErrorVaultUnavailable ErrorKind = "vault_unavailable"

	// Spawn / runtime path
	ErrorContainerCrashed       ErrorKind = "container_crashed"
	ErrorDockerProxyUnreachable ErrorKind = "docker_proxy_unreachable"
	ErrorRateLimitExhausted     ErrorKind = "rate_limit_exhausted"
	ErrorClaudeRuntimeError     ErrorKind = "claude_runtime_error"
	ErrorTurnTimeout            ErrorKind = "turn_timeout"

	// Lifecycle / quota path
	ErrorSessionCostCapReached ErrorKind = "session_cost_cap_reached"
	ErrorSessionEnded          ErrorKind = "session_ended"
	ErrorSessionNotFound       ErrorKind = "session_not_found"

	// Shutdown / restart path
	ErrorSupervisorShutdown ErrorKind = "supervisor_shutdown"
	ErrorSupervisorRestart  ErrorKind = "supervisor_restart"
)

// BuildMCPErrorKind composes the mcp_<server>_<status> form for the
// MCP-health bail path (FR-031). server is the MCP server name from
// the init event ("postgres", "mempalace"); status is whatever Claude
// reported (typically "failed", "error", "disabled", "needs-auth").
// No validation: the supervisor logs verbatim so forensic captures
// match the wire shape.
func BuildMCPErrorKind(server, status string) ErrorKind {
	return fmt.Sprintf("mcp_%s_%s", server, status)
}
