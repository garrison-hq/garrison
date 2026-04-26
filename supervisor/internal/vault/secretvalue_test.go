package vault

import (
	"fmt"
	"strings"
	"testing"
)

func TestSecretValueLogValueReturnsRedacted(t *testing.T) {
	sv := New([]byte("super-secret-value"))
	lv := sv.LogValue()
	if lv.String() != "[REDACTED]" {
		t.Errorf("LogValue() = %q, want %q", lv.String(), "[REDACTED]")
	}
}

func TestSecretValueStringFallbackIsSafe(t *testing.T) {
	raw := "sk-abc123verylongsecretvalue"
	sv := New([]byte(raw))
	formatted := fmt.Sprintf("%s", sv) //nolint:vaultlog // intentional test of fallback formatting
	if strings.Contains(formatted, raw) {
		t.Errorf("fmt.Sprintf(%%s) leaked raw bytes: %q", formatted)
	}
}

func TestSecretValueZeroIdempotent(t *testing.T) {
	sv := New([]byte("secret"))
	sv.Zero()
	if !sv.Empty() {
		t.Error("after Zero(), Empty() should return true")
	}
	// Second call must not panic.
	sv.Zero()
	if !sv.Empty() {
		t.Error("after second Zero(), Empty() should still return true")
	}
}

func TestSecretValueEmptyIsZeroValue(t *testing.T) {
	var sv SecretValue
	if !sv.Empty() {
		t.Error("zero-value SecretValue should be Empty()")
	}
}

func TestSecretValueNewCopiesSource(t *testing.T) {
	src := []byte("original")
	sv := New(src)
	src[0] = 'X' // mutate input
	if sv.UnsafeBytes()[0] == 'X' {
		t.Error("New() did not copy source: mutation of src affected SecretValue")
	}
}
