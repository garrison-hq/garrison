package vault

import (
	"regexp"
	"strings"
)

// redactedPrefix is the opening token of every redaction placeholder.
// Defined as a constant to satisfy the S1192 "duplicate string literal" rule.
const redactedPrefix = "[REDACTED:"

// Label is the pattern name embedded in the [REDACTED:<label>] replacement.
// Each label uniquely identifies which pattern family matched.
type Label string

const (
	LabelSKPrefix      Label = "sk_prefix"
	LabelXOXBPrefix    Label = "xoxb_prefix"
	LabelAWSAKIA       Label = "aws_akia"
	LabelPEMHeader     Label = "pem_header"
	LabelGitHubPAT     Label = "github_pat"     // ghp_
	LabelGitHubApp     Label = "github_app"     // gho_
	LabelGitHubUser    Label = "github_user"    // ghu_
	LabelGitHubServer  Label = "github_server"  // ghs_
	LabelGitHubRefresh Label = "github_refresh" // ghr_
	LabelBearerShape   Label = "bearer_shape"
)

// patterns binds each Label to its compiled regex. Minimum-length gates
// (e.g. {20,}) prevent over-redaction of short prose like "sk-" in isolation.
var patterns = []struct {
	label Label
	re    *regexp.Regexp
}{
	{LabelSKPrefix, regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`)},
	{LabelXOXBPrefix, regexp.MustCompile(`xoxb-[A-Za-z0-9\-]{20,}`)},
	{LabelAWSAKIA, regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{LabelPEMHeader, regexp.MustCompile(`-----BEGIN [A-Z ]+-----`)},
	{LabelGitHubPAT, regexp.MustCompile(`ghp_[A-Za-z0-9]{30,}`)},
	{LabelGitHubApp, regexp.MustCompile(`gho_[A-Za-z0-9]{30,}`)},
	{LabelGitHubUser, regexp.MustCompile(`ghu_[A-Za-z0-9]{30,}`)},
	{LabelGitHubServer, regexp.MustCompile(`ghs_[A-Za-z0-9]{30,}`)},
	{LabelGitHubRefresh, regexp.MustCompile(`ghr_[A-Za-z0-9]{30,}`)},
	{LabelBearerShape, regexp.MustCompile(`(?i)authorization:\s*bearer\s+[A-Za-z0-9\-\._~\+\/]+=*`)},
}

// ScanAndRedact replaces every pattern-match in s with [REDACTED:<label>]
// and returns the labels observed (nil if none matched). It is safe to
// call on arbitrary agent-produced strings. Best-effort per Q7 — a
// secret that does not match any known pattern slips through.
func ScanAndRedact(s string) (redacted string, matched []Label) {
	redacted = s
	for _, p := range patterns {
		if p.re.MatchString(redacted) {
			matched = append(matched, p.label)
			redacted = p.re.ReplaceAllString(redacted, redactedPrefix+string(p.label)+"]")
		}
	}
	return
}

// scanAllFields applies ScanAndRedact to each string in fields, updating
// the slice in place and accumulating matched labels. Convenience helper
// for the finalize handler. Unexported — finalize package accesses via
// ScanAndRedact directly.
func scanAllFields(fields []*string) []Label {
	var all []Label
	for _, f := range fields {
		if f == nil {
			continue
		}
		redacted, matched := ScanAndRedact(*f)
		if len(matched) > 0 {
			*f = redacted
			all = append(all, matched...)
		}
	}
	return all
}

// RedactLabel formats a matched label as the [REDACTED:...] token. Exported
// so tests can construct expected strings without depending on the private
// format string.
func RedactLabel(l Label) string {
	return redactedPrefix + string(l) + "]"
}

// ContainsRedacted reports whether s contains any redaction placeholder,
// regardless of which label. Useful in tests.
func ContainsRedacted(s string) bool {
	return strings.Contains(s, redactedPrefix)
}
