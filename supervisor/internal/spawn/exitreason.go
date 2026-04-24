// exit_reason vocabulary for the agent_instances table. Centralized here so
// every value written to the column is a named constant or the output of one
// of the two helper functions below.
//
// The spec FR-112 and FR-114 reference this vocabulary by the string values
// themselves, not by constant name; the agent_instances.exit_reason column
// is TEXT. Keeping the constants in one file lets a reviewer grep for any
// exit-reason value and find every path that can produce it.
//
// M1's internal/spawn.Classify predates this file and returns string literals
// inline; those literals match the constants below. M2.1 and later code
// should reference the constants.
package spawn

import (
	"fmt"
	"syscall"
)

// Static exit_reason values used on the real-Claude path (T013 and
// downstream). Comments cite the spec/plan clause that mandates each value.
const (
	ExitCompleted          = "completed"           // FR-112 success path.
	ExitClaudeError        = "claude_error"        // result.is_error == true.
	ExitParseError         = "parse_error"         // malformed NDJSON on stdout (FR-106).
	ExitTimeout            = "timeout"             // subprocess context expired (NFR-101).
	ExitSupervisorShutdown = "supervisor_shutdown" // root-context cancel via SIGTERM to supervisor.
	ExitSpawnFailed        = "spawn_failed"        // pre-exec failure (MCP config write, cmd.Start, etc.; FR-103).
	ExitNoResult           = "no_result"           // subprocess exit without parsed result event (FR-110a, clarify Q3).
	ExitAcceptanceFailed   = "acceptance_failed"   // hello.txt missing or contents mismatch (FR-112).
	ExitAgentMissing       = "agent_missing"       // no agents row for the department+role.
	ExitBudgetExceeded     = "budget_exceeded"     // terminal result reports --max-budget-usd overrun (M2.2 NFR-201 / FR-220).

	// M2.2.1 additions (FR-266). The finalize_ticket completion flow
	// introduces a new family of terminal dispositions; each is set on
	// a specific branch of the atomic-write or retry-counter state
	// machine. Comments cite the spec clause that mandates each value.
	ExitFinalizeInvalid            = "finalize_invalid"             // supervisor retry counter hit 3 failed attempts (FR-257, US3).
	ExitFinalizePalaceWriteFailed  = "finalize_palace_write_failed" // MemPalace AddDrawer/AddTriples errored inside the atomic tx (FR-264).
	ExitFinalizeCommitFailed       = "finalize_commit_failed"       // Postgres commit failed after palace writes succeeded (FR-265, orphan case).
	ExitFinalizeWriteTimeout       = "finalize_write_timeout"       // 30s atomic-write ceiling fired (FR-265a).
	ExitFinalizeNeverCalled        = "finalize_never_called"        // subprocess exited without issuing a single finalize_ticket call (US5).
	ExitFinalizeTransitionConflict = "finalize_transition_conflict" // transition row already exists for the ticket (edge case).

	// M2.3 additions (D7.1 / FR-405 / FR-407 / FR-410). Vault-path
	// failure modes; each corresponds to a vault sentinel error or a
	// threat-model rule enforcement point.
	ExitSecretLeakedInAgentMd    = "secret_leaked_in_agent_md"    // FR-407 Rule 1: literal secret value found in agent.md pre-spawn.
	ExitVaultMCPInConfig         = "vault_mcp_in_config"          // FR-410 Rule 3: banned vault/secret/infisical MCP server in config.
	ExitVaultUnavailable         = "vault_unavailable"            // FR-405: Infisical unreachable or transport error.
	ExitVaultAuthExpired         = "vault_auth_expired"           // FR-405: token expired after auto-reauth retry.
	ExitVaultPermissionDenied    = "vault_permission_denied"      // FR-405: Infisical returned 403 Forbidden.
	ExitVaultRateLimited         = "vault_rate_limited"           // FR-405: Infisical returned 429 Too Many Requests.
	ExitVaultSecretNotFound      = "vault_secret_not_found"       // FR-405: secret path does not exist in Infisical.
	ExitVaultAuditFailed         = "vault_audit_failed"           // FR-412 Q9 fail-closed: vault_access_log INSERT failed.

	// M1-inherited values; preserved for the fake-agent path and recovery.
	ExitSupervisorRestart = "supervisor_restarted" // M1 recovery query.
	ExitDepartmentMissing = "department_missing"   // M1 edge case.
)

// FormatMCPFailure returns the exit_reason string for an MCP health-check
// failure: "mcp_<server>_<status>". Empty serverName is rejected with a
// defensive "unknown" fallback — Claude's init event always populates the
// name, so an empty value indicates a supervisor-side bug and we want the
// row to scream that fact rather than emit "mcp__failed" which would be
// visually easy to miss.
func FormatMCPFailure(serverName, status string) string {
	if serverName == "" {
		serverName = "unknown"
	}
	return "mcp_" + serverName + "_" + status
}

// FormatSignalled returns the exit_reason string for a subprocess that was
// terminated by an external signal (not by the supervisor itself). The
// canonical signal name (SIGKILL, SIGTERM, etc.) is used rather than the
// bare number so grep-ing logs for "signaled_SIGKILL" finds every instance
// regardless of platform.
//
// This is the M2.1 counterpart to the M1 signalName() helper in spawn.go;
// real-Claude code paths should use this, fake-agent code paths continue
// using the M1 helper for backwards compatibility with M1 tests.
func FormatSignalled(sig syscall.Signal) string {
	var name string
	switch sig {
	case syscall.SIGKILL:
		name = "SIGKILL"
	case syscall.SIGTERM:
		name = "SIGTERM"
	case syscall.SIGINT:
		name = "SIGINT"
	case syscall.SIGHUP:
		name = "SIGHUP"
	case syscall.SIGQUIT:
		name = "SIGQUIT"
	case syscall.SIGSEGV:
		name = "SIGSEGV"
	case syscall.SIGABRT:
		name = "SIGABRT"
	case syscall.SIGPIPE:
		name = "SIGPIPE"
	default:
		name = fmt.Sprintf("signal_%d", int(sig))
	}
	return "signaled_" + name
}
