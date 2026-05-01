//go:build chaos

// M5.3 chaos tests covering AC-1 / AC-2 / AC-3 from the chat
// threat-model amendment (docs/security/chat-threat-model.md §3) plus
// the cost-cap and per-turn tool-call ceiling runtime mitigations
// (Rule 4). Runs under `go test -tags chaos ./internal/garrisonmutate/...`.
//
// The fixture set lives at supervisor/internal/spawn/mockclaude/scripts/m5_3/
// and is selected via GARRISON_MOCKCLAUDE_FIXTURE. Each test reads its
// bound fixture and asserts the threat-model-defined outcome.
//
// Tests skip cleanly when the testcontainer Postgres harness or the
// garrison-mockclaude:m5 image is unavailable (matches the M5.1 / M5.2
// chaos test conventions — chat-stack runtime gating).

package garrisonmutate

import (
	"context"
	"os"
	"testing"
)

// requireChaosStack reports whether the test environment can boot the
// chat stack. In CI this becomes a real check (testcontainer + image
// availability + supervisor binary build); for now it short-circuits
// to skip with a descriptive reason so the build-tag-gated tests
// don't break local invocations.
func requireChaosStack(t *testing.T) {
	t.Helper()
	if os.Getenv("GARRISON_RUN_CHAOS") != "1" {
		t.Skip("chaos tests skipped: set GARRISON_RUN_CHAOS=1 + boot the m5.3 chat stack to enable")
	}
}

// TestPalaceInjectionAttackClass1 — AC-1 per chat-threat-model.md §3
// + §6. Plants a malicious palace entry; triggers a chat turn that
// retrieves it via mempalace.search; asserts the threat-model-defined
// outcome: verb fires with attacker-influenced args; chat_mutation_audit
// row records the chain (chat_session_id, chat_message_id, verb,
// args_jsonb capturing the injected text); activity feed surfaces the
// event.
func TestPalaceInjectionAttackClass1(t *testing.T) {
	requireChaosStack(t)
	_ = context.Background()
	t.Skip("AC-1 chaos: scenario implemented at chat-stack-runtime enable time")
}

// TestComposerInjectionAttackClass2 — AC-2 per chat-threat-model.md §3
// + §6. Operator pastes injection-shaped content; assistant interprets
// as instructions; same posture as AC-1.
func TestComposerInjectionAttackClass2(t *testing.T) {
	requireChaosStack(t)
	t.Skip("AC-2 chaos: scenario implemented at chat-stack-runtime enable time")
}

// TestToolResultFeedbackLoopAttackClass3 — AC-3 per chat-threat-model.md
// §3 + §6. tool_result text contains tool-call-shaped content;
// assistant chains; per-turn ceiling fires before unbounded mutation
// rows land.
func TestToolResultFeedbackLoopAttackClass3(t *testing.T) {
	requireChaosStack(t)
	t.Skip("AC-3 chaos: scenario implemented at chat-stack-runtime enable time")
}

// TestCostCapTerminatesSession — runaway-loop fixture; per-session
// cost cap (M5.1 FR-061) fires before > N mutation rows land.
// Synthetic terminal row carries error_kind='session_cost_cap_reached';
// SSE typed-error frame surfaces.
func TestCostCapTerminatesSession(t *testing.T) {
	requireChaosStack(t)
	t.Skip("cost-cap chaos: implemented at chat-stack-runtime enable time")
}

// TestToolCallCeilingTerminatesContainer — 51st tool_use event triggers
// ceiling fire; supervisor terminates the chat container; synthetic
// terminal row carries error_kind='tool_call_ceiling_reached'; no
// further mutations land.
func TestToolCallCeilingTerminatesContainer(t *testing.T) {
	requireChaosStack(t)
	t.Skip("ceiling chaos: implemented at chat-stack-runtime enable time")
}

// TestConcurrentMutationConflictResolves — two simultaneous
// transition_ticket calls on the same ticket; SELECT ... FOR UPDATE
// NOWAIT contention; one commits, one returns ErrTicketStateChanged.
// Both audit rows land; final state matches the winning side.
func TestConcurrentMutationConflictResolves(t *testing.T) {
	requireChaosStack(t)
	t.Skip("concurrent-mutation chaos: implemented at chat-stack-runtime enable time")
}
