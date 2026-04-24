// Package fakevault is a testdata stub that mirrors vault.SecretValue's
// API surface so positive/negative testdata packages can import it without
// needing the real supervisor module path inside analysistest.
package fakevault

import "log/slog"

// SecretValue is the fake version; the analyzer detects it by type name
// ("SecretValue") in a package whose path ends with "vault".
type SecretValue struct{ b []byte }

func New(b []byte) SecretValue        { return SecretValue{b: append([]byte(nil), b...)} }
func (v SecretValue) UnsafeBytes() []byte { return v.b }
func (v SecretValue) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }
func (v SecretValue) String() string   { return "[REDACTED]" }
