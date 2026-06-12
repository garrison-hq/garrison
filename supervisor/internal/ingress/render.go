package ingress

import "strings"

// nullBodyFallback is the literal substituted for absent or null body fields
// (FR-102, spike QS4). It is a package-level constant so the GitHub connector
// and the unit tests refer to the same string without duplication.
const nullBodyFallback = "(no description provided)"

// boundedVars is the exact set of template variables the bounded substitution
// engine recognises (plan.md decision 10). Variables outside this set are left
// verbatim — no partial substitution surprises (FR-102).
var boundedVars = []string{"title", "url", "body", "number", "sender"}

// renderTemplate substitutes the bounded variable set
// {{title}}, {{url}}, {{body}}, {{number}}, {{sender}} into tmpl using plain
// strings.ReplaceAll — no text/template engine, no injection surface, no error
// path (plan.md decision 10, FR-102; mirrors schedule.RenderTemplate precedent).
//
// Rules:
//   - Only the five named variables above are substituted. Any other {{token}}
//     in the template string is left verbatim (no partial substitution).
//   - A key that is absent from vars (or whose value is the empty string from a
//     null JSON field) substitutes the nullBodyFallback literal
//     "(no description provided)" rather than the empty string, so ticket text
//     is always readable (spike QS4).
//   - The fallback only fires for absent keys; a key explicitly mapped to a
//     non-empty string uses that string as-is.
func renderTemplate(tmpl string, vars map[string]string) string {
	out := tmpl
	for _, name := range boundedVars {
		token := "{{" + name + "}}"
		if !strings.Contains(out, token) {
			continue
		}
		val, ok := vars[name]
		if !ok || val == "" {
			val = nullBodyFallback
		}
		out = strings.ReplaceAll(out, token, val)
	}
	return out
}
