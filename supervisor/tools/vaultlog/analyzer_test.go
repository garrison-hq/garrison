package vaultlog_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/garrison-hq/garrison/supervisor/tools/vaultlog"
)

func TestAnalyzer(t *testing.T) {
	// testdata/src is the GOPATH-style source root for the fake packages.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	testdata := filepath.Join(wd, "testdata")

	analysistest.Run(t, testdata, vaultlog.Analyzer, "positive")
	analysistest.Run(t, testdata, vaultlog.Analyzer, "negative")
	analysistest.Run(t, testdata, vaultlog.Analyzer, "override")
}
