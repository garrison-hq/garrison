// Package positive contains call sites that the vaultlog analyzer MUST flag.
package positive

import (
	"fmt"
	"log"
	"log/slog"

	"fakevault"
)

func positiveExamples() {
	sv := fakevault.New([]byte("secret"))
	logger := slog.Default()

	slog.Info("x", "v", sv)               // want `vault.SecretValue passed to`
	slog.Info("x", "v", sv.UnsafeBytes()) // want `vault.SecretValue passed to`
	logger.Info("x", "v", sv)             // want `vault.SecretValue passed to`
	fmt.Sprintf("%s", sv)                 // want `vault.SecretValue passed to`
	log.Printf("%v", sv)                  // want `vault.SecretValue passed to`
}
