package garrisonmutate

import (
	"sort"
	"strings"
	"testing"
)

// TestVerbsRegistryMatchesEnumeration is the sealed-allow-list test
// per chat-threat-model.md Rule 1 + spec FR-411 + plan §1.1 (M5.3) +
// FR-103 (M7) + FR-600 (M9). The Verbs slice MUST contain exactly the
// enumerated chat-side verb set (11 as of M9). Adding a verb without
// updating the threat-model amendment + this test fails CI.
func TestVerbsRegistryMatchesEnumeration(t *testing.T) {
	want := []string{
		"create_ticket",
		"edit_ticket",
		"transition_ticket",
		"pause_agent",
		"resume_agent",
		"spawn_agent",
		"edit_agent_config",
		"propose_hire",
		// M7 FR-103 additions:
		"propose_skill_change",
		"bump_skill_version",
		// M9 FR-600 addition (eleventh verb, Tier 3):
		"create_scheduled_task",
	}
	got := VerbNames()
	sort.Strings(got)
	wantSorted := append([]string{}, want...)
	sort.Strings(wantSorted)

	if len(got) != len(wantSorted) {
		t.Fatalf("Verbs has %d entries; want %d. got=%v want=%v", len(got), len(wantSorted), got, wantSorted)
	}
	for i, name := range got {
		if name != wantSorted[i] {
			t.Errorf("Verbs[%d] = %q; want %q", i, name, wantSorted[i])
		}
	}
}

// TestVerbsRegistryHasNoVaultEntries pins the M2.3 Rule 3 carryover:
// vault verbs are NOT in scope for chat. Defense-in-depth against a
// future maintainer accidentally adding one.
func TestVerbsRegistryHasNoVaultEntries(t *testing.T) {
	for _, v := range Verbs {
		lower := strings.ToLower(v.Name)
		for _, banned := range []string{"vault", "secret", "infisical"} {
			if strings.Contains(lower, banned) {
				t.Errorf("verb %q matches banned vault pattern %q", v.Name, banned)
			}
		}
	}
}

// TestVerbsRegistryReversibilityClassesValid asserts every verb has a
// class in {1, 2, 3} matching the chat_mutation_audit.reversibility_class
// CHECK constraint.
func TestVerbsRegistryReversibilityClassesValid(t *testing.T) {
	for _, v := range Verbs {
		if v.ReversibilityClass < 1 || v.ReversibilityClass > 3 {
			t.Errorf("verb %q has reversibility_class=%d; want 1, 2, or 3", v.Name, v.ReversibilityClass)
		}
	}
}

// TestVerbsRegistryAffectedResourceTypes verifies every verb declares a
// supported affected_resource_type matching the audit table's CHECK.
func TestVerbsRegistryAffectedResourceTypes(t *testing.T) {
	allowed := map[string]struct{}{"ticket": {}, "agent_role": {}, "hiring_proposal": {}, "scheduled_task": {}}
	for _, v := range Verbs {
		if _, ok := allowed[v.AffectedResourceType]; !ok {
			t.Errorf("verb %q has affected_resource_type=%q; want one of {ticket, agent_role, hiring_proposal, scheduled_task}",
				v.Name, v.AffectedResourceType)
		}
	}
}

// TestFindVerbReturnsNilForUnknown asserts the dispatch's not-found
// path returns nil rather than a default Verb.
func TestFindVerbReturnsNilForUnknown(t *testing.T) {
	if v := FindVerb("rotate_secret"); v != nil {
		t.Errorf("FindVerb(\"rotate_secret\") = %+v; want nil", v)
	}
	if v := FindVerb(""); v != nil {
		t.Errorf("FindVerb(\"\") = %+v; want nil", v)
	}
}

// TestFindVerbReturnsExpectedEntry asserts FindVerb returns the actual
// registry entry (pointer-to-actual, not a copy of the data).
func TestFindVerbReturnsExpectedEntry(t *testing.T) {
	v := FindVerb("create_ticket")
	if v == nil {
		t.Fatal("FindVerb(\"create_ticket\") returned nil")
	}
	if v.Name != "create_ticket" {
		t.Errorf("FindVerb(\"create_ticket\").Name = %q", v.Name)
	}
	if v.ReversibilityClass != 3 {
		t.Errorf("create_ticket reversibility = %d; want 3", v.ReversibilityClass)
	}
}

// TestVerbsSlicesDisjoint asserts that the chat-side Verbs registry
// and the M8 ServerActionVerbs registry have empty intersection. The
// chat surface MUST NOT expose register_mcp_server (per FR-306 +
// docs/security/chat-threat-model.md), and the Server-Action surface
// MUST NOT expose chat-side verbs (the SA wrapping logic uses
// `FindServerActionVerb`, not `FindVerb`).
func TestVerbsSlicesDisjoint(t *testing.T) {
	chatSet := make(map[string]bool, len(Verbs))
	for _, v := range Verbs {
		chatSet[v.Name] = true
	}
	for _, sa := range ServerActionVerbs {
		if chatSet[sa.Name] {
			t.Errorf("verb %q appears in both Verbs (chat) and ServerActionVerbs", sa.Name)
		}
	}
}

// TestServerActionVerbsTierTable pins the ServerActionVerbs registry to
// the tier table in chat-threat-model.md §5 (Server-Action verb
// registry): the M8 entry plus the four M9 scheduled-task entries, each
// with its amended reversibility class and resource type. Adding or
// re-tiering an entry without amending the threat model + this test
// fails CI (Rule 1 applies to the Server-Action slice too).
func TestServerActionVerbsTierTable(t *testing.T) {
	want := map[string]struct {
		class        int
		resourceType string
	}{
		"register_mcp_server": {2, "mcp_server"},
		// M9 additions:
		"edit_scheduled_task":   {2, "scheduled_task"},
		"pause_scheduled_task":  {1, "scheduled_task"},
		"resume_scheduled_task": {1, "scheduled_task"},
		"delete_scheduled_task": {3, "scheduled_task"},
	}
	if len(ServerActionVerbs) != len(want) {
		t.Fatalf("ServerActionVerbs has %d entries; want %d (%v)", len(ServerActionVerbs), len(want), want)
	}
	seen := make(map[string]bool, len(ServerActionVerbs))
	for _, v := range ServerActionVerbs {
		w, ok := want[v.Name]
		if !ok {
			t.Errorf("unexpected ServerActionVerbs entry %q", v.Name)
			continue
		}
		if seen[v.Name] {
			t.Errorf("duplicate ServerActionVerbs entry %q", v.Name)
		}
		seen[v.Name] = true
		if v.ReversibilityClass != w.class {
			t.Errorf("%s reversibility_class = %d; want %d", v.Name, v.ReversibilityClass, w.class)
		}
		if v.AffectedResourceType != w.resourceType {
			t.Errorf("%s affected_resource_type = %q; want %q", v.Name, v.AffectedResourceType, w.resourceType)
		}
		if v.Handler == nil {
			t.Errorf("%s has nil Handler", v.Name)
		}
	}
}
