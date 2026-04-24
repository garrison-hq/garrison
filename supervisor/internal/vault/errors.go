package vault

import "errors"

// Sentinel errors returned by vault.Client operations. Each maps to an
// exit_reason constant in internal/spawn/exitreason.go (M2.3 additions).
// ClassifyExitReason performs the mapping; callers should not compare the
// string values directly — use errors.Is against these sentinels.
var (
	ErrVaultUnavailable      = errors.New("vault: unavailable")
	ErrVaultAuthExpired      = errors.New("vault: auth expired (after auto-reauth retry)")
	ErrVaultPermissionDenied = errors.New("vault: permission denied by Infisical")
	ErrVaultRateLimited      = errors.New("vault: rate limited")
	ErrVaultSecretNotFound   = errors.New("vault: secret path not found")
	ErrVaultAuditFailed      = errors.New("vault: audit log write failed (fail-closed)")
)

// exit_reason string values — mirrors internal/spawn/exitreason.go M2.3 block.
// Using string literals here avoids a circular import with the spawn package.
// errors_test.go asserts these agree with the spawn constants.
const (
	exitVaultUnavailable      = "vault_unavailable"
	exitVaultAuthExpired      = "vault_auth_expired"
	exitVaultPermissionDenied = "vault_permission_denied"
	exitVaultRateLimited      = "vault_rate_limited"
	exitVaultSecretNotFound   = "vault_secret_not_found"
	exitVaultAuditFailed      = "vault_audit_failed"
	exitClaudeError           = "claude_error"
)

// ClassifyExitReason maps a vault error to the canonical exit_reason string
// value recorded in agent_instances.exit_reason. Uses errors.Is so wrapped
// errors are handled correctly. nil returns "" (caller's no-op signal).
// An unknown non-nil error returns exitClaudeError as a defensive default.
func ClassifyExitReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrVaultUnavailable):
		return exitVaultUnavailable
	case errors.Is(err, ErrVaultAuthExpired):
		return exitVaultAuthExpired
	case errors.Is(err, ErrVaultPermissionDenied):
		return exitVaultPermissionDenied
	case errors.Is(err, ErrVaultRateLimited):
		return exitVaultRateLimited
	case errors.Is(err, ErrVaultSecretNotFound):
		return exitVaultSecretNotFound
	case errors.Is(err, ErrVaultAuditFailed):
		return exitVaultAuditFailed
	default:
		return exitClaudeError
	}
}
