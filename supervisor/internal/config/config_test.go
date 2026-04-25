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
	// M2.2 additions
	"GARRISON_MEMPALACE_CONTAINER",
	"GARRISON_PALACE_PATH",
	"GARRISON_DOCKER_BIN",
	"GARRISON_AGENT_MEMPALACE_PASSWORD",
	"GARRISON_HYGIENE_DELAY",
	"GARRISON_HYGIENE_SWEEP_INTERVAL",
	"DOCKER_HOST",
	// M2.3 additions
	"GARRISON_INFISICAL_ADDR",
	"GARRISON_INFISICAL_CLIENT_ID",
	"GARRISON_INFISICAL_CLIENT_SECRET",
	"GARRISON_INFISICAL_PROJECT_ID",
	"GARRISON_INFISICAL_ENVIRONMENT",
	"GARRISON_CUSTOMER_ID",
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
	// M2.2: real-claude path requires the mempalace-role password.
	t.Setenv("GARRISON_AGENT_MEMPALACE_PASSWORD", "secret-mp-pw")
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())
	// M2.3: Infisical config; use GARRISON_CUSTOMER_ID to bypass the DB query.
	t.Setenv("GARRISON_INFISICAL_ADDR", "http://garrison-infisical:8080")
	t.Setenv("GARRISON_INFISICAL_CLIENT_ID", "test-client-id")
	t.Setenv("GARRISON_INFISICAL_CLIENT_SECRET", "test-client-secret")
	t.Setenv("GARRISON_CUSTOMER_ID", "00000000-0000-0000-0000-000000000001")

	// Build an isolated $PATH containing fake claude + docker binaries so
	// exec.LookPath succeeds without depending on the host having either
	// installed. M2.2 requires both.
	binDir := t.TempDir()
	for _, name := range []string{"claude", "docker"} {
		fakeBin := filepath.Join(binDir, name)
		if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("seed fake %s: %v", name, err)
		}
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
	t.Setenv("GARRISON_AGENT_MEMPALACE_PASSWORD", "secret-mp-pw")
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())
	// Isolated empty PATH so exec.LookPath cannot find claude (or docker —
	// but the claude check fires first in Load's order so we assert on it).
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

// ------------------------------------------------------------------
// M2.2 — T009 tests for the new env vars and validation.
// ------------------------------------------------------------------

func TestM22ConfigDefaults(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo") // skip real-claude preconditions
	// Clear any overrides from the surrounding env.
	t.Setenv("GARRISON_MEMPALACE_CONTAINER", "")
	t.Setenv("GARRISON_PALACE_PATH", "")
	t.Setenv("GARRISON_HYGIENE_DELAY", "")
	t.Setenv("GARRISON_HYGIENE_SWEEP_INTERVAL", "")
	t.Setenv("DOCKER_HOST", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}
	if cfg.ClaudeBudgetUSD != 0.10 {
		t.Errorf("ClaudeBudgetUSD=%v; want 0.10 (NFR-201)", cfg.ClaudeBudgetUSD)
	}
	if cfg.HygieneDelay != 5*time.Second {
		t.Errorf("HygieneDelay=%s; want 5s", cfg.HygieneDelay)
	}
	if cfg.HygieneSweepInterval != 60*time.Second {
		t.Errorf("HygieneSweepInterval=%s; want 60s", cfg.HygieneSweepInterval)
	}
	if cfg.MempalaceContainer != "garrison-mempalace" {
		t.Errorf("MempalaceContainer=%q; want garrison-mempalace", cfg.MempalaceContainer)
	}
	if cfg.PalacePath != "/palace" {
		t.Errorf("PalacePath=%q; want /palace", cfg.PalacePath)
	}
	if cfg.DockerHost != "tcp://garrison-docker-proxy:2375" {
		t.Errorf("DockerHost=%q; want tcp://garrison-docker-proxy:2375", cfg.DockerHost)
	}
}

func TestM22ConfigHonoursDockerHostOverride(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo")
	t.Setenv("DOCKER_HOST", "tcp://foo.example:2375")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}
	if cfg.DockerHost != "tcp://foo.example:2375" {
		t.Errorf("DockerHost=%q; want tcp://foo.example:2375", cfg.DockerHost)
	}
}

func TestM22ConfigRejectsZeroHygieneDelay(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo")
	t.Setenv("GARRISON_HYGIENE_DELAY", "0s")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error on zero hygiene delay")
	}
	if !strings.Contains(err.Error(), "GARRISON_HYGIENE_DELAY") {
		t.Errorf("error should name the env var: %v", err)
	}
}

func TestM22ConfigRejectsNegativeSweepInterval(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo")
	t.Setenv("GARRISON_HYGIENE_SWEEP_INTERVAL", "-5s")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error on negative sweep interval")
	}
	if !strings.Contains(err.Error(), "GARRISON_HYGIENE_SWEEP_INTERVAL") {
		t.Errorf("error should name the env var: %v", err)
	}
}

func TestM22ConfigRequiresMempalacePassword(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	// Not fake-agent; real-path requires GARRISON_AGENT_MEMPALACE_PASSWORD.
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "")
	t.Setenv("GARRISON_AGENT_RO_PASSWORD", "x")
	t.Setenv("GARRISON_AGENT_MEMPALACE_PASSWORD", "")
	// MCPConfigDir and CLAUDE_BIN need something to satisfy Load before
	// hitting the mempalace-password check.
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())
	t.Setenv("GARRISON_CLAUDE_BIN", "/bin/true")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error when GARRISON_AGENT_MEMPALACE_PASSWORD is empty")
	}
	if !strings.Contains(err.Error(), "GARRISON_AGENT_MEMPALACE_PASSWORD") {
		t.Errorf("error should name the env var: %v", err)
	}
}

func TestM22AgentMempalaceDSNComposition(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://orig:origpw@pg.example:5432/garrison")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "")
	t.Setenv("GARRISON_AGENT_RO_PASSWORD", "ro-pw")
	t.Setenv("GARRISON_AGENT_MEMPALACE_PASSWORD", "mp-pw")
	t.Setenv("GARRISON_MCP_CONFIG_DIR", t.TempDir())
	t.Setenv("GARRISON_CLAUDE_BIN", "/bin/true")
	t.Setenv("GARRISON_DOCKER_BIN", "/bin/true")
	// M2.3: Infisical config required on the real-Claude path.
	t.Setenv("GARRISON_INFISICAL_ADDR", "http://garrison-infisical:8080")
	t.Setenv("GARRISON_INFISICAL_CLIENT_ID", "test-id")
	t.Setenv("GARRISON_INFISICAL_CLIENT_SECRET", "test-secret")
	t.Setenv("GARRISON_CUSTOMER_ID", "00000000-0000-0000-0000-000000000001")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}
	dsn := cfg.AgentMempalaceDSN()
	// Expect userinfo replaced with garrison_agent_mempalace:mp-pw
	if !strings.Contains(dsn, "garrison_agent_mempalace") {
		t.Errorf("DSN missing mempalace role: %q", dsn)
	}
	if !strings.Contains(dsn, "mp-pw") {
		t.Errorf("DSN missing mempalace password: %q", dsn)
	}
	if strings.Contains(dsn, "origpw") {
		t.Errorf("DSN still carries original password: %q", dsn)
	}
	if !strings.Contains(dsn, "pg.example:5432/garrison") {
		t.Errorf("DSN missing host/db: %q", dsn)
	}
}

// TestConfigParsesFinalizeWriteTimeout — M2.2.1 T001: a non-default
// GARRISON_FINALIZE_WRITE_TIMEOUT is parsed into cfg.FinalizeWriteTimeout.
func TestConfigParsesFinalizeWriteTimeout(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo")
	t.Setenv("GARRISON_FINALIZE_WRITE_TIMEOUT", "45s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}
	if cfg.FinalizeWriteTimeout != 45*time.Second {
		t.Errorf("FinalizeWriteTimeout = %s; want 45s", cfg.FinalizeWriteTimeout)
	}
}

// TestConfigDefaultFinalizeWriteTimeout — M2.2.1 T001: unset env uses
// the 30-second default per spec Clarification 2026-04-23 Q5.
func TestConfigDefaultFinalizeWriteTimeout(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo")
	t.Setenv("GARRISON_FINALIZE_WRITE_TIMEOUT", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load returned err: %v", err)
	}
	if cfg.FinalizeWriteTimeout != 30*time.Second {
		t.Errorf("FinalizeWriteTimeout = %s; want 30s (default)", cfg.FinalizeWriteTimeout)
	}
}

// TestConfigRejectsNegativeFinalizeTimeout — M2.2.1 T001: non-positive
// values fail fast at startup with a recognisable message.
func TestConfigRejectsNegativeFinalizeTimeout(t *testing.T) {
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@h/db")
	t.Setenv("GARRISON_FAKE_AGENT_CMD", "/bin/echo")
	t.Setenv("GARRISON_FINALIZE_WRITE_TIMEOUT", "-1s")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error on negative FinalizeWriteTimeout")
	}
	if !strings.Contains(err.Error(), "GARRISON_FINALIZE_WRITE_TIMEOUT") {
		t.Errorf("error should name the env var: %v", err)
	}
}

// TestLoadConfigInfisicalAddrOptional — M2.3: when GARRISON_INFISICAL_ADDR is
// unset the supervisor starts without vault (vault is opt-in, not required).
// M2.1/M2.2 integration tests rely on this: they run real-Claude mode without
// vault env vars, so Load() must not fail when addr is absent.
func TestLoadConfigInfisicalAddrOptional(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_ADDR", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error when GARRISON_INFISICAL_ADDR is unset: %v", err)
	}
	if cfg.InfisicalAddr() != "" {
		t.Errorf("InfisicalAddr() = %q; want empty string when env var is unset", cfg.InfisicalAddr())
	}
}

// TestLoadConfigInfisicalClientIDRequired — M2.3 T006.
func TestLoadConfigInfisicalClientIDRequired(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_CLIENT_ID", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error when GARRISON_INFISICAL_CLIENT_ID is unset")
	}
	if !strings.Contains(err.Error(), "GARRISON_INFISICAL_CLIENT_ID") {
		t.Errorf("error = %v; want it to name GARRISON_INFISICAL_CLIENT_ID", err)
	}
}

// TestLoadConfigInfisicalClientSecretRequired — M2.3 T006.
func TestLoadConfigInfisicalClientSecretRequired(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_CLIENT_SECRET", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error when GARRISON_INFISICAL_CLIENT_SECRET is unset")
	}
	if !strings.Contains(err.Error(), "GARRISON_INFISICAL_CLIENT_SECRET") {
		t.Errorf("error = %v; want it to name GARRISON_INFISICAL_CLIENT_SECRET", err)
	}
}

// TestLoadConfigInfisicalProjectIDSet — when GARRISON_INFISICAL_PROJECT_ID
// is present, InfisicalProjectID() returns it; absent → empty string.
func TestLoadConfigInfisicalProjectIDSet(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_PROJECT_ID", "proj-abc123")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.InfisicalProjectID() != "proj-abc123" {
		t.Errorf("InfisicalProjectID() = %q; want %q", cfg.InfisicalProjectID(), "proj-abc123")
	}
}

// TestLoadConfigInfisicalEnvironmentSet — when GARRISON_INFISICAL_ENVIRONMENT
// is present, InfisicalEnvironment() returns it; absent → empty string.
func TestLoadConfigInfisicalEnvironmentSet(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_ENVIRONMENT", "production")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.InfisicalEnvironment() != "production" {
		t.Errorf("InfisicalEnvironment() = %q; want %q", cfg.InfisicalEnvironment(), "production")
	}
}

// TestLoadConfigInfisicalOptionalFieldsAbsent — when neither PROJECT_ID nor
// ENVIRONMENT is set, their accessors return empty strings (both are optional).
func TestLoadConfigInfisicalOptionalFieldsAbsent(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_PROJECT_ID", "")
	t.Setenv("GARRISON_INFISICAL_ENVIRONMENT", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.InfisicalProjectID() != "" {
		t.Errorf("InfisicalProjectID(): want empty, got %q", cfg.InfisicalProjectID())
	}
	if cfg.InfisicalEnvironment() != "" {
		t.Errorf("InfisicalEnvironment(): want empty, got %q", cfg.InfisicalEnvironment())
	}
}

// TestLoadConfigCustomerIDFromEnvOverride — M2.3 T006: GARRISON_CUSTOMER_ID
// bypasses the DB bootstrap query; accessor returns the parsed UUID.
func TestLoadConfigCustomerIDFromEnvOverride(t *testing.T) {
	realAgentEnv(t)
	const rawUUID = "12345678-1234-1234-1234-123456789abc"
	t.Setenv("GARRISON_CUSTOMER_ID", rawUUID)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	got := cfg.CustomerID()
	if !got.Valid {
		t.Fatal("CustomerID().Valid = false; want true")
	}
	gotStr := got.String()
	if gotStr != rawUUID {
		t.Errorf("CustomerID() = %q; want %q", gotStr, rawUUID)
	}
}
