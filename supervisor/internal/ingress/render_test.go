package ingress

import "testing"

// TestRender_AllVars — every bounded variable {{title}}, {{url}}, {{body}},
// {{number}}, {{sender}} is substituted with the supplied map value (FR-102,
// plan.md decision 10).
func TestRender_AllVars(t *testing.T) {
	tmpl := "Title: {{title}} | URL: {{url}} | Body: {{body}} | #{{number}} | From: {{sender}}"
	vars := map[string]string{
		"title":  "Fix the login bug",
		"url":    "https://github.com/example/repo/issues/7",
		"body":   "The login form crashes on mobile.",
		"number": "7",
		"sender": "alice",
	}

	got := renderTemplate(tmpl, vars)
	want := "Title: Fix the login bug | URL: https://github.com/example/repo/issues/7 | Body: The login form crashes on mobile. | #7 | From: alice"
	if got != want {
		t.Errorf("renderTemplate() = %q; want %q", got, want)
	}
}

// TestRender_AbsentVarFallback — a template key that is absent from the vars
// map must substitute the nullBodyFallback literal "(no description provided)"
// rather than an empty string, so that ticket text is always readable (FR-102,
// spike QS4). No error path exists; the function returns a string.
func TestRender_AbsentVarFallback(t *testing.T) {
	// Template references "body" but the vars map does not contain "body".
	tmpl := "Context: {{body}}"
	vars := map[string]string{
		"title": "Some title",
	}

	got := renderTemplate(tmpl, vars)
	want := "Context: " + nullBodyFallback
	if got != want {
		t.Errorf("renderTemplate() = %q; want %q (fallback for absent key)", got, want)
	}
}

// TestRender_AbsentVarFallback_EmptyValue — a key that is present in the map
// but mapped to an empty string (e.g. a null JSON field that was coerced to "")
// also substitutes the fallback literal (FR-102, spike QS4 null-body handling).
func TestRender_AbsentVarFallback_EmptyValue(t *testing.T) {
	tmpl := "Acceptance: {{body}}"
	vars := map[string]string{
		"body": "", // empty string — same as absent for null JSON fields
	}

	got := renderTemplate(tmpl, vars)
	want := "Acceptance: " + nullBodyFallback
	if got != want {
		t.Errorf("renderTemplate() = %q; want %q (fallback for empty-value key)", got, want)
	}
}

// TestRender_UnknownVarPassthrough — a {{token}} that is not in the bounded
// variable set (title, url, body, number, sender) must be left verbatim in
// the output; renderTemplate must not attempt any partial substitution that
// could produce surprising results (plan.md decision 10, FR-102).
func TestRender_UnknownVarPassthrough(t *testing.T) {
	tmpl := "Title: {{title}} | Custom: {{custom_field}} | Random: {{unknown}}"
	vars := map[string]string{
		"title": "My ticket",
	}

	got := renderTemplate(tmpl, vars)
	// {{custom_field}} and {{unknown}} are not in boundedVars; they must stay.
	want := "Title: My ticket | Custom: {{custom_field}} | Random: {{unknown}}"
	if got != want {
		t.Errorf("renderTemplate() = %q; want %q (unknown tokens stay verbatim)", got, want)
	}
}
