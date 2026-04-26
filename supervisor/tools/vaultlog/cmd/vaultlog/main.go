// Command vaultlog runs the vaultlog analyzer as a standalone checker.
// Usage: go run ./tools/vaultlog/cmd/vaultlog ./...
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	vaultlog "github.com/garrison-hq/garrison/supervisor/tools/vaultlog"
)

func main() {
	singlechecker.Main(vaultlog.Analyzer)
}
