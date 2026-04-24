// Package negative contains call sites the vaultlog analyzer must NOT flag.
package negative

import (
	"log/slog"

	"fakevault"
)

func negativeExamples(sv fakevault.SecretValue) {
	slog.Info("x", "path", "/production/stripe/key") // no diagnostic: plain string
	slog.Info("x", "redacted", sv.LogValue())         // no diagnostic: LogValue() is safe
	_ = someFunc(sv)                                   // no diagnostic: not a logging call
}

func someFunc(sv fakevault.SecretValue) bool { return sv.UnsafeBytes() != nil }
