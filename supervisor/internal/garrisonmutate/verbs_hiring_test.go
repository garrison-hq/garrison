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
