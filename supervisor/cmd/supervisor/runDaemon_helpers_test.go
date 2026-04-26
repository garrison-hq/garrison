package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
)

// Tests for the pure helpers extracted from runDaemon during the M3
// quality cleanup. The full daemon path needs Postgres + Docker, but
// these helpers can be exercised directly with config fixtures.

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestResolveSupervisorBinHonorsOverride(t *testing.T) {
	t.Setenv("GARRISON_SUPERVISOR_BIN_OVERRIDE", "/usr/local/bin/garrison-test")
	got := resolveSupervisorBin(newDiscardLogger())
	if got != "/usr/local/bin/garrison-test" {
		t.Errorf("override path: got %q", got)
	}
}

func TestResolveSupervisorBinFallsBackToExecutable(t *testing.T) {
	// Empty override → os.Executable() (or os.Args[0] on failure). On a
	// normal test run os.Executable returns the test binary path, which
	// is fine — we just want a non-empty path that isn't the override.
	t.Setenv("GARRISON_SUPERVISOR_BIN_OVERRIDE", "")
	got := resolveSupervisorBin(newDiscardLogger())
	if got == "" {
		t.Fatal("resolveSupervisorBin returned empty path")
	}
	if got == "/usr/local/bin/garrison-test" {
		t.Errorf("override should not be used; got %q", got)
	}
	// Sanity: returned path should be absolute (test binaries always are).
	if !filepath.IsAbs(got) {
		t.Errorf("expected absolute path, got %q", got)
	}
}

func TestBuildSharedPalaceClientNilWhenFakeAgent(t *testing.T) {
	cfg := &config.Config{UseFakeAgent: true}
	if c := buildSharedPalaceClient(cfg); c != nil {
		t.Errorf("fake-agent: want nil client, got %+v", c)
	}
}

func TestBuildSharedPalaceClientNilWhenBootstrapDisabled(t *testing.T) {
	cfg := &config.Config{DisablePalaceBootstrap: true}
	if c := buildSharedPalaceClient(cfg); c != nil {
		t.Errorf("disable-bootstrap: want nil client, got %+v", c)
	}
}

func TestBuildSharedPalaceClientPopulatedOnRealPath(t *testing.T) {
	cfg := &config.Config{
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "unix:///var/run/docker.sock",
		DockerBin:          "/usr/bin/docker",
	}
	c := buildSharedPalaceClient(cfg)
	if c == nil {
		t.Fatal("real path: want non-nil client")
	}
	if c.MempalaceContainer != "garrison-mempalace" {
		t.Errorf("container: %q", c.MempalaceContainer)
	}
	if c.PalacePath != "/palace" {
		t.Errorf("palace path: %q", c.PalacePath)
	}
	if c.Timeout == 0 {
		t.Error("Timeout should default to 10s, got 0")
	}
	if c.Exec == nil {
		t.Error("Exec should be a RealDockerExec, got nil")
	}
}

func TestBuildVaultClientNilWhenFakeAgent(t *testing.T) {
	cfg := &config.Config{UseFakeAgent: true}
	got, exitCode := buildVaultClient(context.Background(), cfg, newDiscardLogger())
	if got != nil {
		t.Errorf("fake-agent: want nil vault, got %+v", got)
	}
	if exitCode != ExitOK {
		t.Errorf("fake-agent: want ExitOK, got %d", exitCode)
	}
}

func TestBuildVaultClientNilWhenInfisicalAddrUnset(t *testing.T) {
	// Empty Config means InfisicalAddr() returns "". Vault is optional in
	// that case — the M2.3 design has integration tests that run
	// real-Claude WITHOUT vault env vars and rely on this skip.
	cfg := &config.Config{}
	got, exitCode := buildVaultClient(context.Background(), cfg, newDiscardLogger())
	if got != nil {
		t.Errorf("empty addr: want nil vault, got %+v", got)
	}
	if exitCode != ExitOK {
		t.Errorf("empty addr: want ExitOK, got %d", exitCode)
	}
}

func TestLogDaemonStartupConfigDoesNotPanic(t *testing.T) {
	// logDaemonStartupConfig is pure logging; this is a smoke test that
	// the field accesses don't panic on a default Config. The handler
	// discards output, so no log bytes leak into the test runner.
	cfg := &config.Config{}
	logDaemonStartupConfig(newDiscardLogger(), cfg)
}

func TestEnvOverrideSurvivesNonCanonicalPath(t *testing.T) {
	// Defends against an accidental "trim leading whitespace" or
	// "Clean()" being added to resolveSupervisorBin in the future. The
	// override is taken verbatim — operators may pass relative paths
	// in test rigs (T018 chaos test uses /bin/does-not-exist literally).
	const weird = "  /literal-path-with-leading-space"
	t.Setenv("GARRISON_SUPERVISOR_BIN_OVERRIDE", weird)
	got := resolveSupervisorBin(newDiscardLogger())
	if got != weird {
		t.Errorf("override should be passed verbatim; got %q want %q", got, weird)
	}
	// Sanity-check os.Getenv idempotence so this test isn't broken by
	// some other test mutating the env.
	if os.Getenv("GARRISON_SUPERVISOR_BIN_OVERRIDE") != weird {
		t.Error("env was mutated mid-test")
	}
}
