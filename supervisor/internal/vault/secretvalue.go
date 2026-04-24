// Package vault provides Infisical secret fetching, audit writing, and
// output-scanning for Garrison's spawn path. The central invariant:
// secret values never enter log output, MCP configs, or agent prompts.
//
// SecretValue is the only in-memory representation of a fetched secret.
// Its formatter methods are deliberately absent (no String, MarshalText,
// MarshalJSON, MarshalBinary, GoString, Format) so any accidental pass to
// a fmt/slog call falls through to Go's reflection fallback, which prints
// {[...]} without raw bytes. The vaultlog vet analyzer (supervisor/tools/
// vaultlog) additionally rejects slog/fmt calls that take a SecretValue
// argument at compile time.
package vault

import "log/slog"

// SecretValue holds a secret fetched from Infisical. The zero value is
// valid and represents an absent secret (Empty() == true). Never construct
// one with a raw string literal — use New or the vault.Client.Fetch return.
//
// Deliberately missing formatter methods:
//   - No String()        — fmt.Sprintf("%s", sv) prints "{[...]}", not bytes
//   - No MarshalText()   — encoding.TextMarshaler not implemented
//   - No MarshalJSON()   — encoding/json falls back to struct reflection
//   - No MarshalBinary() — encoding.BinaryMarshaler not implemented
//   - No GoString()      — fmt.Sprintf("%#v", sv) prints struct literal
//   - No Format()        — fmt.Formatter not implemented
//
// The only way to reach raw bytes is via UnsafeBytes(), whose use is
// grep-auditable and whitelisted by the vaultlog analyzer.
type SecretValue struct {
	b []byte
}

// New wraps src bytes in a SecretValue. The input slice is copied so
// mutation of src after New returns does not affect the SecretValue.
func New(src []byte) SecretValue { return SecretValue{b: append([]byte(nil), src...)} }

// LogValue satisfies slog.LogValuer so any slog call that receives a
// SecretValue emits "[REDACTED]" instead of the raw bytes.
func (v SecretValue) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// String satisfies fmt.Stringer per FR-403: format methods MUST return the
// redacted placeholder rather than raw bytes. This ensures fmt.Sprintf("%s",
// sv) and similar produce "[REDACTED]" rather than the raw byte content that
// Go's struct-reflection fallback would expose through []byte fields.
// The vaultlog vet analyzer (supervisor/tools/vaultlog) additionally catches
// any slog/fmt call that passes a SecretValue argument at compile time.
func (v SecretValue) String() string { return "[REDACTED]" }

// UnsafeBytes returns the backing slice. This is the only path to the raw
// secret bytes. Every call site must be grep-auditable. The vaultlog vet
// analyzer flags any slog/fmt call that takes UnsafeBytes() as an argument.
//
// Two whitelisted call sites exist in this package:
//   - leakscan.go's RuleOneLeakScan (Rule 1 literal substring match)
//   - client.go's env-var injection (subprocess environment assembly)
func (v SecretValue) UnsafeBytes() []byte { return v.b }

// Zero wipes the backing slice byte-by-byte and nils the pointer. Call via
// defer on every code path that holds a SecretValue beyond its injection
// point. Idempotent — safe to call multiple times.
func (v *SecretValue) Zero() {
	for i := range v.b {
		v.b[i] = 0
	}
	v.b = nil
}

// Empty reports whether the value is the zero value (no bytes).
func (v SecretValue) Empty() bool { return len(v.b) == 0 }
