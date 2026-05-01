package leakscan_test

import (
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/leakscan"
)

// TestScan_RejectsAllPatterns is table-driven over the 10 canonical
// patterns; each case asserts that Scan returns the named category
// for a representative match. Pinning here so adding a new pattern
// requires updating BOTH this table AND the patterns list — bisymmetric
// maintenance prevents silent pattern-set drift.
func TestScan_RejectsAllPatterns(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    leakscan.MatchCategory
	}{
		{"sk-prefix", "key: sk-abcdefghij1234567890abcdef", leakscan.CategorySKPrefix},
		{"slack-bot-token", "token=xoxb-1234567890abcdefghijklmnop", leakscan.CategorySlackBotToken},
		{"aws-access-key", "AKIA1234567890ABCDEF", leakscan.CategoryAWSAccessKey},
		{"pem-header", "-----BEGIN RSA PRIVATE KEY-----", leakscan.CategoryPEMHeader},
		{"github-pat", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", leakscan.CategoryGitHubPAT},
		{"github-server", "ghs_abcdefghijklmnopqrstuvwxyz0123456789", leakscan.CategoryGitHubServerToken},
		{"github-oauth", "gho_abcdefghijklmnopqrstuvwxyz0123456789", leakscan.CategoryGitHubOAuth},
		{"github-refresh", "ghr_abcdefghijklmnopqrstuvwxyz0123456789", leakscan.CategoryGitHubRefresh},
		{"github-user", "ghu_abcdefghijklmnopqrstuvwxyz0123456789", leakscan.CategoryGitHubUserToken},
		{"bearer-shape", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456", leakscan.CategoryBearerShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := leakscan.Scan([]byte(tc.content)); got != tc.want {
				t.Errorf("Scan(%q) = %q; want %q", tc.content, got, tc.want)
			}
		})
	}
}

// TestScan_AcceptsCleanContent: prose without any pattern match
// returns empty MatchCategory.
func TestScan_AcceptsCleanContent(t *testing.T) {
	cases := []string{
		"This is the company.md document. We build agent orchestration.",
		"# Goals\n\nShip M5.4 cleanly.",
		"",
		"Bearer is a valid English word and should not match without the 30+ trailing chars.",
	}
	for _, content := range cases {
		if got := leakscan.Scan([]byte(content)); got != "" {
			t.Errorf("Scan(%q) = %q; want empty", content, got)
		}
	}
}

// TestScan_NeverReturnsSubstring is the Rule 1 backstop. The
// MatchCategory return value is a stable named string; nothing in
// the public API surface carries the matched substring back. We
// assert this structurally by checking that the returned category
// does not appear inside the input content (the category names are
// chosen to be distinct from any plausible secret prefix).
func TestScan_NeverReturnsSubstring(t *testing.T) {
	content := "key: sk-abcdefghij1234567890abcdef and AKIA1234567890ABCDEF and ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	got := leakscan.Scan([]byte(content))
	if got == "" {
		t.Fatal("expected a match for content with multiple secrets")
	}
	if strings.Contains(content, string(got)) {
		t.Errorf("category %q appears as a substring of input %q — Rule 1 violation", got, content)
	}
}

// TestScanAll_ReturnsAllMatches: content with multiple distinct
// patterns returns every matched category in pattern-list order.
func TestScanAll_ReturnsAllMatches(t *testing.T) {
	content := []byte("sk-abcdefghij1234567890abcdef and AKIA1234567890ABCDEF")
	hits := leakscan.ScanAll(content)
	if len(hits) != 2 {
		t.Fatalf("ScanAll returned %d hits; want 2 (sk-prefix + aws-access-key)", len(hits))
	}
	if hits[0] != leakscan.CategorySKPrefix || hits[1] != leakscan.CategoryAWSAccessKey {
		t.Errorf("ScanAll order = %v; want [sk-prefix aws-access-key]", hits)
	}
}

// TestCategories_StableOrder: the test-only Categories() helper
// returns the 10 canonical names in a stable order so test tables
// can pin them.
func TestCategories_StableOrder(t *testing.T) {
	cats := leakscan.Categories()
	if len(cats) != 10 {
		t.Fatalf("Categories() = %d entries; want 10", len(cats))
	}
	if cats[0] != leakscan.CategorySKPrefix {
		t.Errorf("first category = %q; want sk-prefix", cats[0])
	}
	if cats[9] != leakscan.CategoryBearerShape {
		t.Errorf("last category = %q; want bearer-shape", cats[9])
	}
}
