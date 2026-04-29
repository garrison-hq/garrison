package garrisonmutate

import (
	"sort"
	"strings"
	"testing"
)

// TestVerbsRegistryMatchesEnumeration is the sealed-allow-list test
// per chat-threat-model.md Rule 1 + spec FR-411 + plan §1.1. The Verbs
// slice MUST contain exactly the M5.3 enumeration. Adding a verb
// without updating the threat-model amendment + this test fails CI.
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
	allowed := map[string]struct{}{"ticket": {}, "agent_role": {}, "hiring_proposal": {}}
	for _, v := range Verbs {
		if _, ok := allowed[v.AffectedResourceType]; !ok {
			t.Errorf("verb %q has affected_resource_type=%q; want one of {ticket, agent_role, hiring_proposal}",
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
