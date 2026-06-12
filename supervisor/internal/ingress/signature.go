package ingress

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
)

// verifyGitHubSignature verifies the HMAC-SHA256 signature that GitHub
// delivers in the X-Hub-Signature-256 request header (SR1, FR-300).
//
// Verification rules (spike F1):
//   - header must start with the literal prefix "sha256=" (fail-closed on
//     absent/malformed header — an empty header is also caught here).
//   - The hex portion is decoded; a non-hex string returns ErrBadSignature.
//   - The HMAC is computed over rawBody exactly as received, before any JSON
//     re-encoding, using secret as the HMAC key (spike F1.4 — raw body,
//     UTF-8 untouched).
//   - The comparison uses crypto/subtle.ConstantTimeCompare so timing does
//     not leak whether the secret is close or far from correct (SR1).
//
// On any mismatch or malformed header the function returns ErrBadSignature.
// A nil error means the signature is valid.
//
// secret flows from vault.SecretValue.UnsafeBytes() at the call boundary
// only; the tools/vaultlog analyzer forbids logging it (AGENTS.md). The
// secret is read once at ingress.Server construction and held in the
// connector's config struct, not re-fetched per request (plan.md §Signature
// verification, M2.3 fetch-at-boot precedent).
func verifyGitHubSignature(rawBody []byte, header string, secret []byte) error {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return ErrBadSignature
	}
	hexPart := strings.TrimPrefix(header, prefix)
	want, err := hex.DecodeString(hexPart)
	if err != nil {
		return ErrBadSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(rawBody) // nolint:errcheck — hash.Write never returns an error
	got := mac.Sum(nil)
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return ErrBadSignature
	}
	return nil
}
