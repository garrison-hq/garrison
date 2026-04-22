package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
)

const (
	validDBURL   = "postgres://user:pass@localhost:5432/garrison?sslmode=disable"
	validFakeCmd = `sh -c 'echo ok'`
)

var allEnvVars = []string{
	"GARRISON_DATABASE_URL",
	"GARRISON_FAKE_AGENT_CMD",
	"GARRISON_POLL_INTERVAL",
	"GARRISON_SUBPROCESS_TIMEOUT",
	"GARRISON_SHUTDOWN_GRACE",
	"GARRISON_HEALTH_PORT",
	"GARRISON_LOG_LEVEL",
	"GARRISON_CLAUDE_BIN",
	"GARRISON_CLAUDE_MODEL",
	"GARRISON_CLAUDE_BUDGET_USD",
	"GARRISON_MCP_CONFIG_DIR",
	"GARRISON_AGENT_RO_PASSWORD",
}

// clearAll unsets every GARRISON_* env var the config package reads, so a test
// starts from a known-empty environment regardless of the shell it runs in.
// t.Setenv handles restoration on test cleanup.
func clearAll(t *testing.T) {
	t.Helper()
	for _, k := range allEnvVars {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.DatabaseURL != validDBURL {
		t.Errorf("DatabaseURL = %q, want %q", cfg.DatabaseURL, validDBURL)
	}
	if cfg.FakeAgentCmd != validFakeCmd {
		t.Errorf("FakeAgentCmd = %q, want %q", cfg.FakeAgentCmd, validFakeCmd)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", cfg.PollInterval)
	}
	if cfg.SubprocessTimeout != 60*time.Second {
		t.Errorf("SubprocessTimeout = %v, want 60s", cfg.SubprocessTimeout)
	}
	if cfg.ShutdownGrace != 30*time.Second {
		t.Errorf("ShutdownGrace = %v, want 30s", cfg.ShutdownGrace)
	}
	if cfg.HealthPort != 8080 {
		t.Errorf("HealthPort = %d, want 8080", cfg.HealthPort)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want info", cfg.LogLevel)
	}
}

func TestLoadRejectsSubSecondPoll(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_POLL_INTERVAL", "500ms")

	_, err := config.Load()
	if err == nil {
		t.Fatalf("Load(): want error for sub-second poll interval, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_POLL_INTERVAL") {
		t.Errorf("error = %v; want it to mention GARRISON_POLL_INTERVAL", err)
	}
}

func TestLoadRejectsMissingRequired(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)

	_, err := config.Load()
	if err == nil {
		t.Fatalf("Load(): want error for missing GARRISON_DATABASE_URL, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_DATABASE_URL") {
		t.Errorf("error = %v; want it to name the missing var GARRISON_DATABASE_URL", err)
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_LOG_LEVEL", "chatty")

	_, err := config.Load()
	if err == nil {
		t.Fatalf("Load(): want error for invalid log level, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_LOG_LEVEL") {
		t.Errorf("error = %v; want it to mention GARRISON_LOG_LEVEL", err)
	}
}

// realAgentEnv sets every env var required by the real-Claude (non-fake)
// path to valid values, using t.TempDir() for the MCP config dir and a
// synthesized claude binary on an isolated $PATH. Tests that need to
// mutate one of these fields should override after calling this helper.
func realAgentEnv(t *testing.T) {
	t.Helper()
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_AGENT_RO_PASSWORD", "secret-ro-pw")
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())

	// Build an isolated $PATH containing exactly one fake claude binary so
	// exec.LookPath succeeds without depending on the host having claude
	// installed.
	binDir := t.TempDir()
	fakeBin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("seed fake claude: %v", err)
	}
	t.Setenv("PATH", binDir)
}

func TestLoadResolvesClaudeBin(t *testing.T) {
	realAgentEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.ClaudeBin == "" {
		t.Fatalf("ClaudeBin empty; expected $PATH lookup to resolve to a path")
	}
	if filepath.Base(cfg.ClaudeBin) != "claude" {
		t.Errorf("ClaudeBin = %q; want a path whose basename is 'claude'", cfg.ClaudeBin)
	}
}

func TestLoadResolvesClaudeBinOverride(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_CLAUDE_BIN", "/explicit/path/to/claude")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.ClaudeBin != "/explicit/path/to/claude" {
		t.Errorf("ClaudeBin = %q; want the GARRISON_CLAUDE_BIN override value", cfg.ClaudeBin)
	}
}

func TestLoadFailsWhenClaudeMissing(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_AGENT_RO_PASSWORD", "secret-ro-pw")
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())
	// Isolated empty PATH so exec.LookPath cannot find claude.
	t.Setenv("PATH", t.TempDir())

	_, err := config.Load()
	if err == nil {
		t.Fatalf("Load(): want error when claude is unresolvable, got nil")
	}
	const want = "config: cannot find claude binary on $PATH and GARRISON_CLAUDE_BIN is unset"
	if err.Error() != want {
		t.Errorf("error text = %q; want exact %q", err.Error(), want)
	}
}

func TestLoadParsesBudgetUSD(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_CLAUDE_BUDGET_USD", "0.12")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.ClaudeBudgetUSD != 0.12 {
		t.Errorf("ClaudeBudgetUSD = %v; want 0.12", cfg.ClaudeBudgetUSD)
	}

	// Default when unset.
	realAgentEnv(t)
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.ClaudeBudgetUSD != config.DefaultClaudeBudgetUSD {
		t.Errorf("ClaudeBudgetUSD default = %v; want %v", cfg.ClaudeBudgetUSD, config.DefaultClaudeBudgetUSD)
	}
}

func TestLoadRejectsOutOfRangeBudget(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"zero", "0"},
		{"negative", "-0.01"},
		{"one", "1"},
		{"above-one", "1.50"},
		{"not-a-number", "free"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			realAgentEnv(t)
			t.Setenv("GARRISON_CLAUDE_BUDGET_USD", tc.value)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load(%q): want error, got nil", tc.value)
			}
			if !strings.Contains(err.Error(), "GARRISON_CLAUDE_BUDGET_USD") {
				t.Errorf("error = %v; want it to mention GARRISON_CLAUDE_BUDGET_USD", err)
			}
		})
	}
}

func TestAgentRODSNComposition(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_AGENT_RO_PASSWORD", "p@ss w/ special")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	got := cfg.AgentRODSN()
	// The role is fixed; the password may be percent-encoded per RFC 3986.
	if !strings.Contains(got, "garrison_agent_ro:") {
		t.Errorf("AgentRODSN() = %q; want it to carry the garrison_agent_ro user", got)
	}
	if !strings.Contains(got, "@localhost:5432/garrison") {
		t.Errorf("AgentRODSN() = %q; want host/db preserved from DatabaseURL", got)
	}
	if strings.Contains(got, "p@ss w/ special") {
		t.Errorf("AgentRODSN() = %q; password must be URL-encoded, not raw", got)
	}
	// DatabaseURL itself must be untouched.
	if cfg.DatabaseURL != validDBURL {
		t.Errorf("DatabaseURL mutated by AgentRODSN: %q", cfg.DatabaseURL)
	}
}

func TestLoadRequiresAgentROPasswordInRealMode(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("seed fake claude: %v", err)
	}
	t.Setenv("PATH", binDir)

	_, err := config.Load()
	if err == nil {
		t.Fatalf("Load(): want error for missing GARRISON_AGENT_RO_PASSWORD, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_AGENT_RO_PASSWORD") {
		t.Errorf("error = %v; want it to mention GARRISON_AGENT_RO_PASSWORD", err)
	}
}

func TestLoadHonoursFakeAgentFlag(t *testing.T) {
	// In fake-agent mode the M2.1 preconditions (claude binary, MCP dir
	// writability, agent_ro password) are suppressed so M1 tests can keep
	// running against a minimal env. UseFakeAgent=true and FakeAgentCmd are
	// both populated; ClaudeBin is empty; ClaudeBudgetUSD still gets its
	// default because the default is safe without any probing.
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	// Point PATH at an empty dir so claude cannot be found — proves the
	// lookup is skipped when UseFakeAgent is true.
	t.Setenv("PATH", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if !cfg.UseFakeAgent {
		t.Errorf("UseFakeAgent = false; want true when GARRISON_FAKE_AGENT_CMD is set")
	}
	if cfg.FakeAgentCmd != validFakeCmd {
		t.Errorf("FakeAgentCmd = %q; want %q", cfg.FakeAgentCmd, validFakeCmd)
	}
	if cfg.ClaudeBin != "" {
		t.Errorf("ClaudeBin = %q; want empty (resolution skipped in fake-agent mode)", cfg.ClaudeBin)
	}
	if cfg.ClaudeBudgetUSD != config.DefaultClaudeBudgetUSD {
		t.Errorf("ClaudeBudgetUSD = %v; want default %v", cfg.ClaudeBudgetUSD, config.DefaultClaudeBudgetUSD)
	}
	if cfg.AgentRODSN() == "" {
		t.Errorf("AgentRODSN() = empty; want a DSN even when password is empty (host/db still composed)")
	}
}
