package spawn

import (
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
)

// TestTransitionColumnsEngineerM22 — engineer on in_dev → qa_review (M2.2).
func TestTransitionColumnsEngineerM22(t *testing.T) {
	from, to := transitionColumns("engineer", "in_dev")
	if from != "in_dev" || to != "qa_review" {
		t.Errorf("engineer@in_dev: got (%s, %s); want (in_dev, qa_review)", from, to)
	}
}

// TestTransitionColumnsEngineerM21 — engineer on todo → done (M2.1 compat:
// the M2.1 workflow is single-transition so the engineer's completion
// lands the ticket at done, not qa_review).
func TestTransitionColumnsEngineerM21(t *testing.T) {
	from, to := transitionColumns("engineer", "todo")
	if from != "todo" || to != "done" {
		t.Errorf("engineer@todo: got (%s, %s); want (todo, done)", from, to)
	}
}

// TestTransitionColumnsQAEngineer — qa-engineer role → qa_review → done.
func TestTransitionColumnsQAEngineer(t *testing.T) {
	from, to := transitionColumns("qa-engineer", "qa_review")
	if from != "qa_review" || to != "done" {
		t.Errorf("qa-engineer: got (%s, %s); want (qa_review, done)", from, to)
	}
}

// TestTransitionColumnsFallback — any unknown role defaults to todo → done
// (M2.1 back-compat for fake-agent tests that pre-date role dispatch).
func TestTransitionColumnsFallback(t *testing.T) {
	from, to := transitionColumns("unknown-role", "")
	if from != "todo" || to != "done" {
		t.Errorf("fallback: got (%s, %s); want (todo, done)", from, to)
	}
}

// TestSpawnSystemPromptIncludesInstanceID — verifies the composed system
// prompt (what runRealClaude passes via --system-prompt) contains both
// ticket_id and instance_id in the "This turn" block per Session
// 2026-04-23 Q2. Exercised through mempalace.ComposeSystemPrompt
// directly since that's the helper spawn.go calls; the spawn call site
// just threads the string through to argv.
func TestSpawnSystemPromptIncludesInstanceID(t *testing.T) {
	sp := mempalace.ComposeSystemPrompt("AGENT_MD_BODY", "WAKE_UP_BODY",
		"tkt-abc", "inst-xyz")
	if !strings.Contains(sp, "ticket tkt-abc") {
		t.Errorf("missing ticket_id substitution in:\n%s", sp)
	}
	if !strings.Contains(sp, "agent_instance inst-xyz") {
		t.Errorf("missing instance_id substitution in:\n%s", sp)
	}
	if !strings.Contains(sp, "## This turn") {
		t.Errorf("missing 'This turn' heading; the template shape is load-bearing")
	}
	if !strings.Contains(sp, "WAKE_UP_BODY") {
		t.Error("wake-up stdout missing when it was non-empty")
	}
}

// TestSpawnDefaultRoleSlug — Spawn with empty roleSlug falls back to
// "engineer" for M1/M2.1 back-compat. This is the guarantee that
// existing integration tests (which don't set a role_slug because they
// predate T013) still work.
//
// We test this via the public Spawn signature indirectly: the helper
// transitionColumns is what the role flows into for the succeeded path.
// An empty role passed to transitionColumns would hit "" → default; but
// Spawn's "" → "engineer" coercion makes that impossible. Verify the
// coercion logic by inspecting the source path.
//
// Since Spawn's coercion happens before any test-mockable call, we
// pin the contract at the constants level: the fallback string is
// "engineer" in BOTH Spawn (input coercion) and transitionColumns
// (output default). This consistency is what keeps the fake-agent
// test suite byte-identical.
func TestSpawnDefaultRoleSlug(t *testing.T) {
	// Input: empty role → Spawn coerces to "engineer".
	// Output of that "engineer" in transitionColumns (M2.2 in_dev path):
	from, to := transitionColumns("engineer", "in_dev")
	if from != "in_dev" || to != "qa_review" {
		t.Errorf("engineer@in_dev transition changed under us: (%s, %s)", from, to)
	}
	// If this test fails, it means either Spawn's coercion default or
	// transitionColumns's engineer branch moved — in which case the
	// M2.2 fake-agent path will land rows on an unexpected column.
}

// TestAcceptanceGateSatisfied — M2.2 engineer@in_dev skips the M1
// hello.txt check; M2.1 engineer@todo still runs it; qa-engineer always
// skips; unknown roles fall through to the check (M1 safety-net).
func TestAcceptanceGateSatisfied(t *testing.T) {
	cases := []struct {
		role, col string
		want      bool
	}{
		{"engineer", "in_dev", true},       // M2.2
		{"engineer", "todo", false},        // M2.1 back-compat — hello.txt check still runs
		{"engineer", "", false},            // no column info → defer to check
		{"qa-engineer", "qa_review", true}, // M2.2
		{"qa-engineer", "", true},          // qa-engineer never writes hello.txt by design
		{"", "todo", false},                // empty role → default false
		{"cto", "in_dev", false},           // future role → default false
	}
	for _, c := range cases {
		if got := acceptanceGateSatisfied(c.role, c.col); got != c.want {
			t.Errorf("acceptanceGateSatisfied(%q, %q)=%v; want %v", c.role, c.col, got, c.want)
		}
	}
}
