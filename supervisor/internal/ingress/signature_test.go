package ingress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// computeDigest is a test helper that produces the correct sha256= header
// value for the given body and secret so tests can construct golden inputs.
func computeDigest(body, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// TestVerifyGitHubSignature_Valid — a golden body + secret + correctly
// precomputed "sha256=" digest must return nil (SR1, FR-300).
func TestVerifyGitHubSignature_Valid(t *testing.T) {
	body := []byte(`{"action":"opened","issue":{"id":12345}}`)
	secret := []byte("super-secret-webhook-token")
	header := computeDigest(body, secret)

	if err := verifyGitHubSignature(body, header, secret); err != nil {
		t.Errorf("verifyGitHubSignature() = %v; want nil for a valid signature", err)
	}
}

// TestVerifyGitHubSignature_Mismatch — a correctly formed "sha256=" header
// with the wrong hex value must return ErrBadSignature (SR1).
func TestVerifyGitHubSignature_Mismatch(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	secret := []byte("correct-secret")

	// Build a digest for a *different* secret so the hex is structurally valid
	// but does not match the computed HMAC for the real secret.
	wrongHeader := computeDigest(body, []byte("wrong-secret"))

	err := verifyGitHubSignature(body, wrongHeader, secret)
	if err != ErrBadSignature {
		t.Errorf("verifyGitHubSignature() = %v; want ErrBadSignature on mismatched digest", err)
	}
}

// TestVerifyGitHubSignature_MissingPrefix — a header that does not start
// with "sha256=" must return ErrBadSignature (fail-closed, FR-300).
func TestVerifyGitHubSignature_MissingPrefix(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	secret := []byte("secret")

	// Produce a valid hex string but omit the required "sha256=" prefix.
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	bare := hex.EncodeToString(mac.Sum(nil)) // no "sha256=" prefix

	err := verifyGitHubSignature(body, bare, secret)
	if err != ErrBadSignature {
		t.Errorf("verifyGitHubSignature() = %v; want ErrBadSignature when prefix is absent", err)
	}
}

// TestVerifyGitHubSignature_EmptyHeader — a completely missing/empty header
// must return ErrBadSignature; the function must fail-closed, not pass
// (FR-300 fail-closed requirement, spike F1).
func TestVerifyGitHubSignature_EmptyHeader(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	secret := []byte("secret")

	err := verifyGitHubSignature(body, "", secret)
	if err != ErrBadSignature {
		t.Errorf("verifyGitHubSignature() = %v; want ErrBadSignature on empty header", err)
	}
}

// TestVerifyGitHubSignature_RawBodyExact — signature verification must be
// performed over the exact raw bytes received from the network; re-encoding
// the parsed JSON and verifying over that must fail (spike F1 edge case,
// FR-300). This test captures the raw bytes as a literal string and checks
// that an alternate encoding (even semantically equivalent) produces a
// different HMAC and is therefore rejected.
func TestVerifyGitHubSignature_RawBodyExact(t *testing.T) {
	// rawBody simulates the exact bytes that arrived over the wire — compact
	// JSON with no trailing whitespace.
	rawBody := []byte(`{"action":"opened","issue":{"id":42}}`)
	secret := []byte("hook-secret")

	// correctHeader is computed over the exact raw bytes.
	correctHeader := computeDigest(rawBody, secret)

	// Verify that the raw body + correct header passes.
	if err := verifyGitHubSignature(rawBody, correctHeader, secret); err != nil {
		t.Fatalf("verifyGitHubSignature() = %v; raw body + correct header must pass", err)
	}

	// reEncoded simulates what you would get if the handler JSON-decoded and
	// then re-encoded the body (different byte ordering / whitespace is
	// possible). Even a single added space changes the HMAC.
	reEncoded := []byte(`{"action": "opened", "issue": {"id": 42}}`)

	// The header was computed over rawBody — it must NOT verify reEncoded.
	if err := verifyGitHubSignature(reEncoded, correctHeader, secret); err == nil {
		t.Error("verifyGitHubSignature() = nil; re-encoded body must NOT verify against a raw-body digest")
	}
}
