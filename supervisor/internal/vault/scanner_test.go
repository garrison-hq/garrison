package vault

import (
	"testing"
)

// positive inputs — one per pattern, realistic shaped string that matches
var scanPositiveCases = []struct {
	label Label
	input string
}{
	{LabelSKPrefix, "sk-abcdefghijklmnopqrst123456"},
	{LabelXOXBPrefix, "xoxb-123456789012-123456789012"},
	{LabelAWSAKIA, "AKIAIOSFODNN7EXAMPLE"},
	{LabelPEMHeader, "-----BEGIN RSA PRIVATE KEY-----"},
	{LabelGitHubPAT, "ghp_abcdefghijklmnopqrstuvwxyz0123"},
	{LabelGitHubApp, "gho_abcdefghijklmnopqrstuvwxyz0123"},
	{LabelGitHubUser, "ghu_abcdefghijklmnopqrstuvwxyz0123"},
	{LabelGitHubServer, "ghs_abcdefghijklmnopqrstuvwxyz0123"},
	{LabelGitHubRefresh, "ghr_abcdefghijklmnopqrstuvwxyz0123"},
	{LabelBearerShape, "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
}

// negative inputs — one per pattern, short or wrong shape that must NOT match
var scanNegativeCases = []struct {
	label Label
	input string
}{
	{LabelSKPrefix, "sk-short"},       // too short (< 20 chars after prefix)
	{LabelXOXBPrefix, "xoxb-short"},   // too short
	{LabelAWSAKIA, "AKIAshort"},       // too short (< 16 uppercase+digits)
	{LabelPEMHeader, "BEGIN RSA KEY"}, // no leading dashes
	{LabelGitHubPAT, "ghp_tooshort"},  // < 30 chars after prefix
	{LabelGitHubApp, "gho_tooshort"},
	{LabelGitHubUser, "ghu_tooshort"},
	{LabelGitHubServer, "ghs_tooshort"},
	{LabelGitHubRefresh, "ghr_tooshort"},
	{LabelBearerShape, "Bearer token"}, // no "Authorization:" prefix
}

func TestScanAndRedact_Positive(t *testing.T) {
	for _, tc := range scanPositiveCases {
		t.Run(string(tc.label), func(t *testing.T) {
			redacted, matched := ScanAndRedact(tc.input)
			if len(matched) == 0 {
				t.Errorf("expected match for %q, got none", tc.input)
			}
			found := false
			for _, m := range matched {
				if m == tc.label {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected label %q in matched %v", tc.label, matched)
			}
			if redacted == tc.input {
				t.Errorf("expected redaction to change the string, but it was unchanged: %q", redacted)
			}
		})
	}
}

func TestScanAndRedact_Negative(t *testing.T) {
	for _, tc := range scanNegativeCases {
		t.Run(string(tc.label), func(t *testing.T) {
			redacted, matched := ScanAndRedact(tc.input)
			// A negative case must NOT match the pattern for its label.
			// It may still match another pattern, so we check the specific label.
			for _, m := range matched {
				if m == tc.label {
					t.Errorf("pattern %q should NOT match %q but did (redacted: %q)", tc.label, tc.input, redacted)
				}
			}
		})
	}
}

func TestScanAndRedact_MultipleMatchesInOneString(t *testing.T) {
	input := "sk-abcdefghijklmnopqrst1234567 and ghp_abcdefghijklmnopqrstuvwxyz0123"
	redacted, matched := ScanAndRedact(input)
	if len(matched) < 2 {
		t.Errorf("expected at least 2 matches, got %d: %v", len(matched), matched)
	}
	hasSK, hasGHP := false, false
	for _, m := range matched {
		if m == LabelSKPrefix {
			hasSK = true
		}
		if m == LabelGitHubPAT {
			hasGHP = true
		}
	}
	if !hasSK {
		t.Error("expected LabelSKPrefix in matched")
	}
	if !hasGHP {
		t.Error("expected LabelGitHubPAT in matched")
	}
	if redacted == input {
		t.Error("expected both patterns to be redacted from the output")
	}
}

func TestScanAndRedact_NoMatch(t *testing.T) {
	input := "This is plain English prose with no secret patterns at all."
	redacted, matched := ScanAndRedact(input)
	if matched != nil {
		t.Errorf("expected nil matched for clean prose, got %v", matched)
	}
	if redacted != input {
		t.Errorf("expected output byte-identical to input, got %q", redacted)
	}
}

func TestScanAndRedact_FalsePositiveShortString(t *testing.T) {
	input := "sk-"
	_, matched := ScanAndRedact(input)
	for _, m := range matched {
		if m == LabelSKPrefix {
			t.Errorf("3-char string %q should NOT match LabelSKPrefix (minimum-length gate)", input)
		}
	}
}
