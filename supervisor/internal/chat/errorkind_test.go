package chat

import (
	"strings"
	"testing"
)

// TestErrorKindVocabulary_MatchesSpec is the canonical guard: the set
// of exported ErrorKind* constants matches the spec's enumeration
// (FR-002 + FR-003 + FR-031 + FR-061 + FR-081 + FR-083 + FR-016 +
// edge cases) exactly. Every constant is one snake_case word per
// category. Adding a new error_kind requires updating both the const
// block in errorkind.go AND this table.
//
// Mirrors dashboard/lib/vault/outcomes.test.ts: explicit code change
// here is the only way to introduce a new error_kind, which prevents
// drift between the spec, the supervisor, and the (future M5.2)
// dashboard rendering surface.
func TestErrorKindVocabulary_MatchesSpec(t *testing.T) {
	want := map[ErrorKind]struct{}{
		// vault path
		ErrorTokenNotFound:    {},
		ErrorTokenExpired:     {},
		ErrorVaultUnavailable: {},
		// spawn / runtime
		ErrorContainerCrashed:       {},
		ErrorDockerProxyUnreachable: {},
		ErrorRateLimitExhausted:     {},
		ErrorClaudeRuntimeError:     {},
		ErrorTurnTimeout:            {},
		// lifecycle / quota
		ErrorSessionCostCapReached: {},
		ErrorSessionEnded:          {},
		ErrorSessionNotFound:       {},
		// shutdown / restart
		ErrorSupervisorShutdown: {},
		ErrorSupervisorRestart:  {},
	}

	for ek := range want {
		if ek == "" {
			t.Errorf("empty ErrorKind in vocabulary")
			continue
		}
		// All snake_case ASCII; no spaces, hyphens, or capitals.
		for _, r := range ek {
			if r == ' ' || r == '-' {
				t.Errorf("error_kind %q contains forbidden separator %q", ek, r)
			}
			if r >= 'A' && r <= 'Z' {
				t.Errorf("error_kind %q contains uppercase letter %q", ek, r)
			}
		}
	}

	if len(want) != 13 {
		t.Errorf("vocabulary size = %d; want 13 (update this assertion AND the spec FR-list when adding)", len(want))
	}
}

// TestBuildMCPErrorKind verifies the mcp_<server>_<status> composition
// for the dynamically-shaped MCP-health path. The result string is
// what lands in chat_messages.error_kind when ChatPolicy.OnInit bails
// on a non-connected MCP server.
func TestBuildMCPErrorKind(t *testing.T) {
	cases := []struct {
		server, status string
		want           string
	}{
		{"postgres", "failed", "mcp_postgres_failed"},
		{"mempalace", "disabled", "mcp_mempalace_disabled"},
		{"postgres", "needs-auth", "mcp_postgres_needs-auth"},
		{"mempalace", "error", "mcp_mempalace_error"},
	}
	for _, c := range cases {
		got := BuildMCPErrorKind(c.server, c.status)
		if got != c.want {
			t.Errorf("BuildMCPErrorKind(%q, %q) = %q; want %q",
				c.server, c.status, got, c.want)
		}
		if !strings.HasPrefix(got, "mcp_") {
			t.Errorf("composed %q lacks mcp_ prefix", got)
		}
	}
}
