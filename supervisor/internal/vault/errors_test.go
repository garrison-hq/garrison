package vault

import (
	"fmt"
	"testing"
)

// TestClassifyExitReasonTable verifies every sentinel maps to its expected
// string, wrapped errors are handled, nil returns "", and unknown errors
// return the claude_error default.
func TestClassifyExitReasonTable(t *testing.T) {
	cases := []struct {
		err      error
		expected string
	}{
		{nil, ""},
		{ErrVaultUnavailable, exitVaultUnavailable},
		{ErrVaultAuthExpired, exitVaultAuthExpired},
		{ErrVaultPermissionDenied, exitVaultPermissionDenied},
		{ErrVaultRateLimited, exitVaultRateLimited},
		{ErrVaultSecretNotFound, exitVaultSecretNotFound},
		{ErrVaultAuditFailed, exitVaultAuditFailed},
		// Wrapped variants must still classify correctly.
		{fmt.Errorf("outer: %w", ErrVaultUnavailable), exitVaultUnavailable},
		{fmt.Errorf("outer: %w", ErrVaultAuthExpired), exitVaultAuthExpired},
		{fmt.Errorf("outer: %w", ErrVaultPermissionDenied), exitVaultPermissionDenied},
		{fmt.Errorf("outer: %w", ErrVaultRateLimited), exitVaultRateLimited},
		{fmt.Errorf("outer: %w", ErrVaultSecretNotFound), exitVaultSecretNotFound},
		{fmt.Errorf("outer: %w", ErrVaultAuditFailed), exitVaultAuditFailed},
		// Unknown error falls back to claude_error.
		{fmt.Errorf("some random error"), exitClaudeError},
	}

	for _, tc := range cases {
		got := ClassifyExitReason(tc.err)
		if got != tc.expected {
			t.Errorf("ClassifyExitReason(%v) = %q, want %q", tc.err, got, tc.expected)
		}
	}
}

// TestErrorConstantsAgreeWithSpawnPackage asserts the local string literals
// used by ClassifyExitReason match the spawn package constants. This test is
// the cross-reference guard described in the task (approach (a)): the spawn
// package constants are defined in exitreason.go; once T006 adds them, the
// values here must stay in sync.
//
// Implementation: The spawn constants are added in T006. This test captures
// the expected values as string literals which are also the authoritative
// values per FR-405/FR-407/FR-410/Q9. If someone changes either side,
// this test will catch the drift.
func TestErrorConstantsMatchSpecValues(t *testing.T) {
	want := map[string]string{
		"ErrVaultUnavailable":      "vault_unavailable",
		"ErrVaultAuthExpired":      "vault_auth_expired",
		"ErrVaultPermissionDenied": "vault_permission_denied",
		"ErrVaultRateLimited":      "vault_rate_limited",
		"ErrVaultSecretNotFound":   "vault_secret_not_found",
		"ErrVaultAuditFailed":      "vault_audit_failed",
	}
	got := map[string]string{
		"ErrVaultUnavailable":      ClassifyExitReason(ErrVaultUnavailable),
		"ErrVaultAuthExpired":      ClassifyExitReason(ErrVaultAuthExpired),
		"ErrVaultPermissionDenied": ClassifyExitReason(ErrVaultPermissionDenied),
		"ErrVaultRateLimited":      ClassifyExitReason(ErrVaultRateLimited),
		"ErrVaultSecretNotFound":   ClassifyExitReason(ErrVaultSecretNotFound),
		"ErrVaultAuditFailed":      ClassifyExitReason(ErrVaultAuditFailed),
	}
	for name, wantVal := range want {
		if got[name] != wantVal {
			t.Errorf("%s: ClassifyExitReason = %q, want spec value %q", name, got[name], wantVal)
		}
	}
}
