package skillinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// SHA256Hex computes the hex-encoded SHA-256 of body. Used by the
// install actuator at every download to compute the actual digest;
// VerifyDigest then compares against the propose-time digest.
func SHA256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// VerifyDigest returns nil iff sha256Hex(body) == expected.
// Otherwise wraps ErrDigestMismatch with both digests so the audit
// row can record what differed (FR-106 + HR-7).
func VerifyDigest(body []byte, expected string) error {
	got := SHA256Hex(body)
	if got != expected {
		return fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, expected, got)
	}
	return nil
}
