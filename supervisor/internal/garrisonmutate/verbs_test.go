package garrisonmutate

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestVerbsRegistryMatchesEnumeration is the sealed-allow-list test
// per chat-threat-model.md Rule 1 + spec FR-411 + plan §1.1 (M5.3) +
// FR-103 (M7) + FR-600 (M9) + FR-001 (M11). The Verbs slice MUST
// contain exactly the enumerated chat-side verb set (12 as of M11).
// Adding a verb without updating the threat-model amendment + this
// test fails CI.
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
		// M11 FR-001 addition (twelfth verb, Tier 3, agent-callers only):
		"request_external_action",
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
// M11 adds "pending_action" for request_external_action (FR-001).
func TestVerbsRegistryAffectedResourceTypes(t *testing.T) {
	allowed := map[string]struct{}{
		"ticket": {}, "agent_role": {}, "hiring_proposal": {},
		"scheduled_task": {}, "pending_action": {},
	}
	for _, v := range Verbs {
		if _, ok := allowed[v.AffectedResourceType]; !ok {
			t.Errorf("verb %q has affected_resource_type=%q; want one of {ticket, agent_role, hiring_proposal, scheduled_task, pending_action}",
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
// registry): the M8 entry plus the four M9 scheduled-task entries plus
// the three M11 outbox-action entries, each with its amended
// reversibility class and resource type. Adding or re-tiering an entry
// without amending the threat model + this test fails CI (Rule 1 applies
// to the Server-Action slice too).
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
		// M11 FR-026/FR-027 additions (Outbox approval surface):
		"approve_action":   {1, "pending_action"},
		"reject_action":    {1, "pending_action"},
		"mark_action_done": {1, "pending_action"},
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

// ----------------------------------------------------------------------------
// create_scheduled_task DB-free paths (M9 T020 top-up): arg parsing
// and the subprocess-side min-interval resolution. The DB-backed
// happy/reject paths live in verbs_scheduled_test.go (integration).
// ----------------------------------------------------------------------------

// TestCreateScheduledTaskHandlerRejectsUnparsableArgs — malformed JSON
// maps to validation_failed before any transaction is opened (the
// zero-value Deps would panic on Pool use; reaching the parse
// rejection proves the ordering).
func TestCreateScheduledTaskHandlerRejectsUnparsableArgs(t *testing.T) {
	r, err := realCreateScheduledTaskHandler(context.Background(), Deps{}, json.RawMessage(`{"name":`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Fatal("unparsable args should be rejected")
	}
	if r.ErrorKind != string(ErrValidationFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrValidationFailed)
	}
	if !strings.Contains(r.Message, "parse args") {
		t.Errorf("Message = %q; want the parse detail", r.Message)
	}
}

// TestParseCreateScheduledTaskArgsRequiresIdentityFields — each
// identity field is required after trimming; the rejection names the
// missing field.
func TestParseCreateScheduledTaskArgsRequiresIdentityFields(t *testing.T) {
	base := map[string]string{
		"name":                         "standup",
		"department_slug":              "engineering",
		"role_slug":                    "engineering.engineer",
		"mode":                         "ticket",
		"schedule_expr":                "daily@09:00",
		"objective_template":           "Run the standup",
		"acceptance_criteria_template": "Summary posted",
	}
	for _, field := range []string{"name", "department_slug", "role_slug", "schedule_expr"} {
		t.Run(field, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			m[field] = "   " // whitespace-only trims to empty
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			args, res := parseCreateScheduledTaskArgs(raw)
			if res == nil {
				t.Fatalf("parse accepted empty %s: %+v", field, args)
			}
			if res.Success {
				t.Error("Success = true on missing identity field")
			}
			if !strings.Contains(res.Message, field+" is required") {
				t.Errorf("Message = %q; want %q named", res.Message, field)
			}
		})
	}
}

// TestParseCreateScheduledTaskArgsTrimsFields — surrounding whitespace
// on identity fields is trimmed, not rejected.
func TestParseCreateScheduledTaskArgsTrimsFields(t *testing.T) {
	raw := json.RawMessage(`{"name":"  standup  ","department_slug":" engineering ","role_slug":" engineering.engineer ","mode":" ticket ","schedule_expr":" daily@09:00 ","objective_template":"o","acceptance_criteria_template":"a"}`)
	args, res := parseCreateScheduledTaskArgs(raw)
	if res != nil {
		t.Fatalf("parse rejected: %+v", res)
	}
	if args.Name != "standup" || args.DepartmentSlug != "engineering" ||
		args.RoleSlug != "engineering.engineer" || args.Mode != "ticket" ||
		args.ScheduleExpr != "daily@09:00" {
		t.Errorf("fields not trimmed: %+v", args)
	}
}

// TestSchedVerbMinIntervalResolution — the subprocess-side FR-404
// bound: env value when parseable and positive, config default
// otherwise (malformed values degrade with a warning, never fail the
// verb).
func TestSchedVerbMinIntervalResolution(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{name: "unset uses default", env: "", want: defaultSchedMinInterval},
		{name: "valid value wins", env: "30m", want: 30 * time.Minute},
		{name: "malformed degrades to default", env: "soon", want: defaultSchedMinInterval},
		{name: "non-positive degrades to default", env: "-5m", want: defaultSchedMinInterval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GARRISON_SCHED_MIN_INTERVAL", tc.env)
			if tc.env == "" {
				// t.Setenv("", "") keeps the var present-but-empty,
				// which schedVerbMinInterval treats as unset.
				t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "")
			}
			deps := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			if got := schedVerbMinInterval(deps); got != tc.want {
				t.Errorf("schedVerbMinInterval(%q) = %s; want %s", tc.env, got, tc.want)
			}
		})
	}
	// nil Logger must not panic on the warning path.
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "bogus")
	if got := schedVerbMinInterval(Deps{}); got != defaultSchedMinInterval {
		t.Errorf("schedVerbMinInterval with nil logger = %s; want %s", got, defaultSchedMinInterval)
	}
}
