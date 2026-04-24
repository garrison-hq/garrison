// Package override verifies that a //nolint:vaultlog directive suppresses the
// diagnostic on the flagged line.
package override

import (
	"log/slog"

	"fakevault"
)

func overrideExamples(sv fakevault.SecretValue) {
	slog.Info("x", "v", sv) //nolint:vaultlog -- intentional unsafe print; no diagnostic expected
}
