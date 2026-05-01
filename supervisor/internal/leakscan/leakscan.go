// Package leakscan centralises the secret-shape pattern set used by
// every Garrison surface that accepts operator- or agent-authored
// content into Postgres / MinIO / MemPalace. The same 10 patterns are
// applied across:
//
//   - M2.3 vault Rule 1 (no agent.md may contain a verbatim secret)
//     via internal/finalize.scanAndRedactPayload.
//   - M5.3 chat-driven edit_agent_config Rule 1 carryover via
//     internal/garrisonmutate's scanForSecrets.
//   - M5.4 Company.md save Rule 1 carryover via
//     internal/objstore.Scan.
//
// Centralising here lets all three call sites share the canonical
// pattern list — adding a new pattern is one edit, not three. The
// MatchCategory shape is intentionally narrow: it carries the
// pattern's semantic name (e.g. "aws-access-key") but NEVER the
// matched substring, so callers can surface the rejection without
// echoing the offending value back into logs or UI.
package leakscan

import "regexp"

// MatchCategory is the named class of secret pattern that matched.
// Stable strings — surfaced to the dashboard editor on save-rejection,
// pinned by tests, intentionally short.
type MatchCategory string

const (
	CategorySKPrefix          MatchCategory = "sk-prefix"
	CategorySlackBotToken     MatchCategory = "slack-bot-token"
	CategoryAWSAccessKey      MatchCategory = "aws-access-key"
	CategoryPEMHeader         MatchCategory = "pem-header"
	CategoryGitHubPAT         MatchCategory = "github-pat"
	CategoryGitHubServerToken MatchCategory = "github-server-token"
	CategoryGitHubOAuth       MatchCategory = "github-oauth"
	CategoryGitHubRefresh     MatchCategory = "github-refresh-token"
	CategoryGitHubUserToken   MatchCategory = "github-user-token"
	CategoryBearerShape       MatchCategory = "bearer-shape"
)

type pattern struct {
	category MatchCategory
	re       *regexp.Regexp
}

// patterns is the canonical 10-pattern set. Order matters only for
// determinism in test pinning — Scan returns the FIRST matching
// category so the operator-facing rejection message names a specific
// pattern (not the whole set).
var patterns = []pattern{
	{CategorySKPrefix, regexp.MustCompile(`sk-[A-Za-z0-9_\-]{20,}`)},
	{CategorySlackBotToken, regexp.MustCompile(`xoxb-[A-Za-z0-9_\-]{20,}`)},
	{CategoryAWSAccessKey, regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{CategoryPEMHeader, regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----`)},
	{CategoryGitHubPAT, regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`)},
	{CategoryGitHubServerToken, regexp.MustCompile(`ghs_[A-Za-z0-9]{36}`)},
	{CategoryGitHubOAuth, regexp.MustCompile(`gho_[A-Za-z0-9]{36}`)},
	{CategoryGitHubRefresh, regexp.MustCompile(`ghr_[A-Za-z0-9]{36}`)},
	{CategoryGitHubUserToken, regexp.MustCompile(`ghu_[A-Za-z0-9]{36}`)},
	{CategoryBearerShape, regexp.MustCompile(`Bearer [A-Za-z0-9_\-\.]{30,}`)},
}

// Scan returns the FIRST pattern category that matched, or "" when the
// content is clean. Callers that need every match (e.g. for logging
// the count) iterate via ScanAll instead.
//
// Per Rule 1 (M2.3 / M5.3 / M5.4): the return value carries the
// pattern's semantic category but NOT the matched substring. Surfacing
// the matched bytes back into a log line or UI would defeat the
// scan — the operator/agent can scan their own buffer for the offending
// substring locally.
func Scan(content []byte) MatchCategory {
	for _, p := range patterns {
		if p.re.Match(content) {
			return p.category
		}
	}
	return ""
}

// ScanAll returns every category that matched at least once, in
// pattern-list order, deduplicated. Empty slice means clean.
// Used by tests and by callers that want to log the count of distinct
// matches (still without the substrings).
func ScanAll(content []byte) []MatchCategory {
	var hits []MatchCategory
	for _, p := range patterns {
		if p.re.Match(content) {
			hits = append(hits, p.category)
		}
	}
	return hits
}

// Categories returns the canonical pattern-category list in stable
// order. Test-only surface for table-driven tests across the call sites.
func Categories() []MatchCategory {
	out := make([]MatchCategory, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, p.category)
	}
	return out
}
