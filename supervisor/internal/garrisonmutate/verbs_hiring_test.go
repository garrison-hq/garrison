package garrisonmutate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestProposeHireValidatesRequiredFields covers FR-414 + FR-422
// validation paths.
func TestProposeHireValidatesRequiredFields(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{}`, "role_title is required"},
		{`{"role_title":"SEO specialist"}`, "department_slug is required"},
		{`{"role_title":"SEO specialist","department_slug":"growth"}`, "justification_md is required"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			expectValidationFailure(t, realProposeHireHandler, c.body, c.want)
		})
	}
}

// TestProposeHireRejectsOversizeFields covers length bounds.
func TestProposeHireRejectsOversizeFields(t *testing.T) {
	long := strings.Repeat("x", 10001)
	body, _ := json.Marshal(map[string]string{
		"role_title":       "SEO specialist",
		"department_slug":  "growth",
		"justification_md": long,
	})
	expectValidationFailure(t, realProposeHireHandler, string(body),
		"justification_md exceeds")

	tooLongTitle := strings.Repeat("y", 101)
	body, _ = json.Marshal(map[string]string{
		"role_title":       tooLongTitle,
		"department_slug":  "growth",
		"justification_md": "real reason",
	})
	expectValidationFailure(t, realProposeHireHandler, string(body),
		"role_title exceeds")
}

// TestProposeHireRegistryRealHandler verifies init() ran.
func TestProposeHireRegistryRealHandler(t *testing.T) {
	v := FindVerb("propose_hire")
	if v == nil {
		t.Fatal("FindVerb(propose_hire) = nil")
	}
	r, _ := v.Handler(context.Background(), validationDeps(), json.RawMessage(`{not json`))
	if strings.Contains(r.Message, "not yet implemented") {
		t.Error("verb propose_hire still using stubHandler")
	}
}

// TestProposeSkillChangeValidatesRequiredFields walks the FR-103
// validation surface: missing role_slug, missing justification, no
// add/remove/bump entries. Each path returns ErrValidationFailed
// without touching the DB.
func TestProposeSkillChangeValidatesRequiredFields(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{}`, "agent_role_slug is required"},
		{`{"agent_role_slug":"engineering.engineer"}`, "justification_md is required"},
		{`{"agent_role_slug":"engineering.engineer","justification_md":"why"}`, "at least one of add/remove/bump"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			expectValidationFailure(t, realProposeSkillChangeHandler, c.body, c.want)
		})
	}
}

// TestProposeSkillChangeRejectsBadDigest covers the SHA-256 hex
// validation on add[].digest entries. M7 requires propose-time digests
// for Add/Bump per HR-7.
func TestProposeSkillChangeRejectsBadDigest(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "add missing digest",
			body: `{"agent_role_slug":"engineering.engineer","justification_md":"why","add":[{"package":"skills.sh/x"}]}`,
			want: "add[0].digest is required",
		},
		{
			name: "add too short digest",
			body: `{"agent_role_slug":"engineering.engineer","justification_md":"why","add":[{"package":"skills.sh/x","digest":"abc"}]}`,
			want: "must be 64-char SHA-256 hex",
		},
		{
			name: "uppercase digest rejected",
			body: `{"agent_role_slug":"engineering.engineer","justification_md":"why","add":[{"package":"skills.sh/x","digest":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}]}`,
			want: "must be 64-char SHA-256 hex",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expectValidationFailure(t, realProposeSkillChangeHandler, c.body, c.want)
		})
	}
}

// TestProposeSkillChangeRejectsTooManyEntries pins the
// maxSkillEntriesPerProposal cap. Defends the audit row against
// pathological argument shape.
func TestProposeSkillChangeRejectsTooManyEntries(t *testing.T) {
	entries := make([]map[string]string, 0, maxSkillEntriesPerProposal+1)
	digest := strings.Repeat("a", skillDigestHexLen)
	for i := 0; i <= maxSkillEntriesPerProposal; i++ {
		entries = append(entries, map[string]string{
			"package": "skills.sh/sample",
			"digest":  digest,
		})
	}
	body, _ := json.Marshal(map[string]any{
		"agent_role_slug":  "engineering.engineer",
		"justification_md": "real reason",
		"add":              entries,
	})
	expectValidationFailure(t, realProposeSkillChangeHandler, string(body), "too many entries")
}

// TestBumpSkillVersionValidatesRequiredFields walks the bump_skill_version
// validation surface.
func TestBumpSkillVersionValidatesRequiredFields(t *testing.T) {
	digest := strings.Repeat("a", skillDigestHexLen)
	cases := []struct {
		body string
		want string
	}{
		{`{}`, "agent_role_slug is required"},
		{`{"agent_role_slug":"engineering.engineer"}`, "package is required"},
		{`{"agent_role_slug":"engineering.engineer","package":"skills.sh/x"}`, "to_version is required"},
		{`{"agent_role_slug":"engineering.engineer","package":"skills.sh/x","to_version":"v1.1.0"}`, "to_digest is required"},
		{`{"agent_role_slug":"engineering.engineer","package":"skills.sh/x","to_version":"v1.1.0","to_digest":"abc"}`, "must be 64-char SHA-256 hex"},
		{
			body: `{"agent_role_slug":"engineering.engineer","package":"skills.sh/x","to_version":"v1.1.0","to_digest":"` + digest + `","from_digest":"abc"}`,
			want: "from_digest must be 64-char SHA-256 hex when supplied",
		},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			expectValidationFailure(t, realBumpSkillVersionHandler, c.body, c.want)
		})
	}
}

// TestSkillChangeRegistryRealHandler verifies init() wired
// propose_skill_change to its real handler.
func TestSkillChangeRegistryRealHandler(t *testing.T) {
	v := FindVerb("propose_skill_change")
	if v == nil {
		t.Fatal("FindVerb(propose_skill_change) = nil")
	}
	r, _ := v.Handler(context.Background(), validationDeps(), json.RawMessage(`{not json`))
	if strings.Contains(r.Message, "not yet implemented") {
		t.Error("verb propose_skill_change still using stubHandler")
	}
}

// TestBumpSkillVersionRegistryRealHandler verifies init() wired
// bump_skill_version to its real handler.
func TestBumpSkillVersionRegistryRealHandler(t *testing.T) {
	v := FindVerb("bump_skill_version")
	if v == nil {
		t.Fatal("FindVerb(bump_skill_version) = nil")
	}
	r, _ := v.Handler(context.Background(), validationDeps(), json.RawMessage(`{not json`))
	if strings.Contains(r.Message, "not yet implemented") {
		t.Error("verb bump_skill_version still using stubHandler")
	}
}

// TestUpdateAgentMDIsNotChatVerb pins the F3-lean rule: update_agent_md
// is a Server-Action-only mutation, not a chat verb. The chat-side
// registry must NOT expose it (Rule 1 — sealed verb set).
func TestUpdateAgentMDIsNotChatVerb(t *testing.T) {
	if v := FindVerb("update_agent_md"); v != nil {
		t.Errorf("update_agent_md should not be a registered chat verb (got %+v)", v)
	}
	for _, name := range VerbNames() {
		if name == "update_agent_md" {
			t.Errorf("VerbNames() leaks update_agent_md; chat must not surface it")
		}
	}
}

// TestLooksLikeSHA256Hex pins the digest format check used by both
// new verbs.
func TestLooksLikeSHA256Hex(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"too short", "abc", false},
		{"upper rejected", strings.Repeat("A", 64), false},
		{"non-hex rejected", strings.Repeat("g", 64), false},
		{"happy path", strings.Repeat("a", 64), true},
		{"mixed digits + a-f", "0123456789abcdef" + strings.Repeat("0", 48), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeSHA256Hex(c.in); got != c.want {
				t.Errorf("looksLikeSHA256Hex(%q) = %v; want %v", c.in, got, c.want)
			}
		})
	}
}

// TestJustificationOrPlaceholder pins the helper used by
// bump_skill_version when the operator omits a justification.
func TestJustificationOrPlaceholder(t *testing.T) {
	if got := justificationOrPlaceholder("real reason", "bump_skill_version"); got != "real reason" {
		t.Errorf("non-empty input mutated: %q", got)
	}
	if got := justificationOrPlaceholder("   ", "bump_skill_version"); !strings.Contains(got, "bump_skill_version") {
		t.Errorf("placeholder missing verb name: %q", got)
	}
}
