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
