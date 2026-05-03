package skillinstall

import (
	"errors"
	"testing"
)

// TestSHA256MatchesKnown — pin SHA-256 of "hello" against the
// canonical hex string.
func TestSHA256MatchesKnown(t *testing.T) {
	got := SHA256Hex([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("got %s; want %s", got, want)
	}
}

// TestVerifyDigestHappyPath — body's actual hash matches expected.
func TestVerifyDigestHappyPath(t *testing.T) {
	body := []byte("hello")
	hash := SHA256Hex(body)
	if err := VerifyDigest(body, hash); err != nil {
		t.Errorf("VerifyDigest: %v", err)
	}
}

// TestVerifyDigestMismatch — body doesn't match expected; surfaces
// ErrDigestMismatch with both digests in the error message.
func TestVerifyDigestMismatch(t *testing.T) {
	body := []byte("hello")
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	err := VerifyDigest(body, wrong)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("err: got %v; want ErrDigestMismatch", err)
	}
	// Message must mention BOTH the expected and actual hashes so
	// the audit row records what differed.
	msg := err.Error()
	if !contains(msg, wrong) || !contains(msg, SHA256Hex(body)) {
		t.Errorf("error message missing one of the digests: %s", msg)
	}
}

// TestSHA256OfEmpty — pin SHA-256 of empty bytes (the canonical
// e3b0c44... value) so a regression in the helper would fail loud.
func TestSHA256OfEmpty(t *testing.T) {
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := SHA256Hex(nil); got != want {
		t.Errorf("nil: %s; want %s", got, want)
	}
	if got := SHA256Hex([]byte{}); got != want {
		t.Errorf("empty: %s; want %s", got, want)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
