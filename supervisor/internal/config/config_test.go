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
	// M5.4 additions
	"GARRISON_MINIO_ENDPOINT",
	"GARRISON_MINIO_BUCKET",
	"GARRISON_MINIO_USE_TLS",
	"GARRISON_MINIO_ACCESS_KEY_PATH",
	"GARRISON_MINIO_SECRET_KEY_PATH",
	"GARRISON_DASHBOARD_API_PORT",
	// M8 additions
	"GARRISON_MCPJUNGLE_URL",
	"GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH",
	// M7.1 additions
	"GARRISON_AGENTS_NETWORK",
	"GARRISON_EGRESS_PROXY_URL",
	"GARRISON_USE_DIRECT_EXEC",
	// M9 additions
	"GARRISON_SCHED_TICK_INTERVAL",
	"GARRISON_SCHED_MIN_INTERVAL",
	"GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN",
	// M10 additions
	"GARRISON_INGRESS_PORT",
	"GARRISON_INGRESS_GITHUB_ENABLED",
	"GARRISON_INGRESS_GITHUB_CONNECTOR_ID",
	"GARRISON_INGRESS_GITHUB_DEPARTMENT",
	"GARRISON_INGRESS_GITHUB_RATE_PER_MIN",
	"GARRISON_INGRESS_GITHUB_BURST",
	// M11 additions
	"GARRISON_ACTION_GITHUB_PAT_PATH",
	"GARRISON_ACTION_POLL_INTERVAL",
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
	// M5.4: MinIO sidecar.
	t.Setenv("GARRISON_MINIO_ENDPOINT", "garrison-minio:9000")
	t.Setenv("GARRISON_MINIO_ACCESS_KEY_PATH", "/operator/MINIO_ACCESS_KEY")
	t.Setenv("GARRISON_MINIO_SECRET_KEY_PATH", "/operator/MINIO_SECRET_KEY")

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
	// M5.4: MinIO sidecar.
	t.Setenv("GARRISON_MINIO_ENDPOINT", "garrison-minio:9000")
	t.Setenv("GARRISON_MINIO_ACCESS_KEY_PATH", "/operator/MINIO_ACCESS_KEY")
	t.Setenv("GARRISON_MINIO_SECRET_KEY_PATH", "/operator/MINIO_SECRET_KEY")

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

// TestLoadConfigInfisicalClientIDAndSecretAccessors — InfisicalClientID() and
// InfisicalClientSecret() return the values from env vars when set.
func TestLoadConfigInfisicalClientIDAndSecretAccessors(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_INFISICAL_CLIENT_ID", "my-client-id")
	t.Setenv("GARRISON_INFISICAL_CLIENT_SECRET", "my-client-secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): unexpected error: %v", err)
	}
	if cfg.InfisicalClientID() != "my-client-id" {
		t.Errorf("InfisicalClientID() = %q; want %q", cfg.InfisicalClientID(), "my-client-id")
	}
	if cfg.InfisicalClientSecret() != "my-client-secret" {
		t.Errorf("InfisicalClientSecret() = %q; want %q", cfg.InfisicalClientSecret(), "my-client-secret")
	}
}

// TestLoadConfigParseLogLevelWarnErrorDebug — all four valid log levels
// parse without error; invalid values already tested by TestLoadRejectsInvalidLogLevel.
func TestLoadConfigParseLogLevelWarnErrorDebug(t *testing.T) {
	cases := []struct {
		level string
		want  slog.Level
	}{
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"debug", slog.LevelDebug},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			realAgentEnv(t)
			t.Setenv("GARRISON_LOG_LEVEL", tc.level)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load(): unexpected error for level %q: %v", tc.level, err)
			}
			if cfg.LogLevel != tc.want {
				t.Errorf("LogLevel = %v; want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

// TestLoadConfigCustomerIDInvalidUUID — GARRISON_CUSTOMER_ID set to a
// non-UUID value fails Load with a recognisable message (covers the
// id.Scan error branch in the env-override path).
func TestLoadConfigCustomerIDInvalidUUID(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_CUSTOMER_ID", "not-a-uuid")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error for invalid GARRISON_CUSTOMER_ID, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_CUSTOMER_ID") {
		t.Errorf("error = %v; want it to mention GARRISON_CUSTOMER_ID", err)
	}
}

// TestLoadConfigCustomerIDFromDBUnreachable — when GARRISON_CUSTOMER_ID is
// unset and the DB pool fails to query (unreachable host), Load returns the
// resolveCustomerID error. Exercises the resolveCustomerID DB path.
func TestLoadConfigCustomerIDFromDBUnreachable(t *testing.T) {
	realAgentEnv(t)
	// Point at an unreachable Postgres so resolveCustomerID's QueryRow fails.
	// The pool is created lazily (no immediate dial); the SELECT then errors.
	t.Setenv("GARRISON_DATABASE_URL", "postgres://u:p@127.0.0.1:1/garrison?sslmode=disable&connect_timeout=1")
	t.Setenv("GARRISON_CUSTOMER_ID", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error from unreachable resolveCustomerID, got nil")
	}
	if !strings.Contains(err.Error(), "CustomerID") {
		t.Errorf("error = %v; want it to mention CustomerID", err)
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

// ----------------------------------------------------------------------------
// M5.4 — MinIO sidecar + dashboard-facing HTTP server config.
// ----------------------------------------------------------------------------

func TestLoadM5_4_RejectsMissingMinIOEndpoint(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_MINIO_ENDPOINT", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "GARRISON_MINIO_ENDPOINT") {
		t.Errorf("expected MinIO endpoint required error; got %v", err)
	}
}

func TestLoadM5_4_RejectsMissingAccessKeyPath(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_MINIO_ACCESS_KEY_PATH", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "GARRISON_MINIO_ACCESS_KEY_PATH") {
		t.Errorf("expected access-key-path required error; got %v", err)
	}
}

func TestLoadM5_4_RejectsMissingSecretKeyPath(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_MINIO_SECRET_KEY_PATH", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "GARRISON_MINIO_SECRET_KEY_PATH") {
		t.Errorf("expected secret-key-path required error; got %v", err)
	}
}

func TestLoadM5_4_DefaultsBucket(t *testing.T) {
	realAgentEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.MinIOBucket != config.DefaultMinIOBucket {
		t.Errorf("MinIOBucket = %q; want %q", cfg.MinIOBucket, config.DefaultMinIOBucket)
	}
}

func TestLoadM5_4_DefaultsUseTLSFalse(t *testing.T) {
	realAgentEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.MinIOUseTLS {
		t.Errorf("MinIOUseTLS = true; want false")
	}
}

func TestLoadM5_4_DefaultsDashboardAPIPort(t *testing.T) {
	realAgentEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DashboardAPIPort != config.DefaultDashboardAPIPort {
		t.Errorf("DashboardAPIPort = %d; want %d", cfg.DashboardAPIPort, config.DefaultDashboardAPIPort)
	}
}

func TestLoadM5_4_AcceptsExplicitDashboardAPIPort(t *testing.T) {
	realAgentEnv(t)
	t.Setenv("GARRISON_DASHBOARD_API_PORT", "9090")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DashboardAPIPort != 9090 {
		t.Errorf("DashboardAPIPort = %d; want 9090", cfg.DashboardAPIPort)
	}
}

// -------- M6 T013 env-var parsing -----------------------------------------

// TestM6EnvVarsParseDefaults — when none of the M6 env vars are set,
// the config carries the documented defaults.
func TestM6EnvVarsParseDefaults(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxTicketsPerTurn != 10 {
		t.Errorf("MaxTicketsPerTurn = %d; want 10", cfg.MaxTicketsPerTurn)
	}
	if cfg.ThinDiaryThreshold != 200 {
		t.Errorf("ThinDiaryThreshold = %d; want 200", cfg.ThinDiaryThreshold)
	}
	if cfg.DefaultSpawnCostUSD != "0.05" {
		t.Errorf("DefaultSpawnCostUSD = %q; want 0.05", cfg.DefaultSpawnCostUSD)
	}
	if cfg.RateLimitBackOff != 60*time.Second {
		t.Errorf("RateLimitBackOff = %v; want 60s", cfg.RateLimitBackOff)
	}
	if cfg.FinalizeResultGrace != 3*time.Second {
		t.Errorf("FinalizeResultGrace = %v; want 3s", cfg.FinalizeResultGrace)
	}
}

// TestM6EnvVarsParseOverrides — every env var is honoured when set
// to a valid value.
func TestM6EnvVarsParseOverrides(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_CHAT_MAX_TICKETS_PER_TURN", "5")
	t.Setenv("GARRISON_HYGIENE_THIN_DIARY_THRESHOLD", "150")
	t.Setenv("GARRISON_DEFAULT_SPAWN_COST_USD", "0.20")
	t.Setenv("GARRISON_RATE_LIMIT_BACK_OFF_SECONDS", "120")
	t.Setenv("GARRISON_FINALIZE_RESULT_GRACE", "5s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxTicketsPerTurn != 5 {
		t.Errorf("MaxTicketsPerTurn = %d; want 5", cfg.MaxTicketsPerTurn)
	}
	if cfg.ThinDiaryThreshold != 150 {
		t.Errorf("ThinDiaryThreshold = %d; want 150", cfg.ThinDiaryThreshold)
	}
	if cfg.DefaultSpawnCostUSD != "0.20" {
		t.Errorf("DefaultSpawnCostUSD = %q; want 0.20", cfg.DefaultSpawnCostUSD)
	}
	if cfg.RateLimitBackOff != 120*time.Second {
		t.Errorf("RateLimitBackOff = %v; want 120s", cfg.RateLimitBackOff)
	}
	if cfg.FinalizeResultGrace != 5*time.Second {
		t.Errorf("FinalizeResultGrace = %v; want 5s", cfg.FinalizeResultGrace)
	}
}

// TestM6EnvVarsRejectInvalidMaxTickets — 0 or negative values are
// rejected (the per-turn ceiling must be at least 1).
func TestM6EnvVarsRejectInvalidMaxTickets(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		t.Run("v="+v, func(t *testing.T) {
			clearAll(t)
			t.Setenv("GARRISON_DATABASE_URL", validDBURL)
			t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
			t.Setenv("GARRISON_CHAT_MAX_TICKETS_PER_TURN", v)
			if _, err := config.Load(); err == nil {
				t.Errorf("expected error for value %q", v)
			}
		})
	}
}

// TestM6EnvVarsRejectInvalidCostUSD — negative cost is rejected.
func TestM6EnvVarsRejectInvalidCostUSD(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_DEFAULT_SPAWN_COST_USD", "-0.10")
	if _, err := config.Load(); err == nil {
		t.Error("expected error for negative cost")
	}
}

// TestM6EnvVarsRejectInvalidBackOff — zero seconds is rejected
// (the back-off must be > 0 to be meaningful).
func TestM6EnvVarsRejectInvalidBackOff(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_RATE_LIMIT_BACK_OFF_SECONDS", "0")
	if _, err := config.Load(); err == nil {
		t.Error("expected error for back-off=0")
	}
}

// TestM6EnvVarsParseFinalizeGraceDuration — valid duration shapes are
// accepted; "3s", "500ms", "0s" all parse cleanly.
func TestM6EnvVarsParseFinalizeGraceDuration(t *testing.T) {
	for _, v := range []string{"3s", "500ms", "0s"} {
		t.Run("v="+v, func(t *testing.T) {
			clearAll(t)
			t.Setenv("GARRISON_DATABASE_URL", validDBURL)
			t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
			t.Setenv("GARRISON_FINALIZE_RESULT_GRACE", v)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load %q: %v", v, err)
			}
			want, _ := time.ParseDuration(v)
			if cfg.FinalizeResultGrace != want {
				t.Errorf("FinalizeResultGrace = %v; want %v", cfg.FinalizeResultGrace, want)
			}
		})
	}
}

// TestM8MCPJungleEnvVarsDefault — when no MCPJungle env vars are set,
// the config carries the documented defaults: URL empty (forcing
// operator-explicit configuration in non-fake-agent mode), admin
// token path "mcpjungle/admin".
func TestM8MCPJungleEnvVarsDefault(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MCPJungleURL != "" {
		t.Errorf("MCPJungleURL default = %q; want empty", cfg.MCPJungleURL)
	}
	if cfg.MCPJungleAdminTokenPath != "mcpjungle/admin" {
		t.Errorf("MCPJungleAdminTokenPath = %q; want mcpjungle/admin", cfg.MCPJungleAdminTokenPath)
	}
}

// TestAgentsNetworkAndEgressProxyDefaults — M7.1 knobs (plan §8): with
// no env set, the agents network defaults to the compose network name
// and the egress proxy URL to the squid sidecar's service DNS.
func TestAgentsNetworkAndEgressProxyDefaults(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentsNetwork != "garrison-agents" {
		t.Errorf("AgentsNetwork default = %q; want garrison-agents", cfg.AgentsNetwork)
	}
	if cfg.EgressProxyURL != "http://garrison-egress-proxy:3128" {
		t.Errorf("EgressProxyURL default = %q; want http://garrison-egress-proxy:3128", cfg.EgressProxyURL)
	}
}

// TestUseDirectExecDefaultsFalse — M7.1 T012, the milestone's single
// behavior flip (FR-018): container exec is the default; setting
// GARRISON_USE_DIRECT_EXEC=true is the rollback lever and must still
// parse (the flag is NOT removed — post-soak polish per the M7 retro).
func TestUseDirectExecDefaultsFalse(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UseDirectExec {
		t.Error("UseDirectExec default = true; want false (T012 flip)")
	}

	t.Setenv("GARRISON_USE_DIRECT_EXEC", "true")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load with GARRISON_USE_DIRECT_EXEC=true: %v", err)
	}
	if !cfg.UseDirectExec {
		t.Error("GARRISON_USE_DIRECT_EXEC=true did not override; rollback lever broken")
	}
}

// TestAgentsNetworkAndEgressProxyOverrides — env overrides land in the
// config struct verbatim.
func TestAgentsNetworkAndEgressProxyOverrides(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_AGENTS_NETWORK", "garrison-agents-staging")
	t.Setenv("GARRISON_EGRESS_PROXY_URL", "http://egress-staging:3128")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AgentsNetwork != "garrison-agents-staging" {
		t.Errorf("AgentsNetwork = %q; want override", cfg.AgentsNetwork)
	}
	if cfg.EgressProxyURL != "http://egress-staging:3128" {
		t.Errorf("EgressProxyURL = %q; want override", cfg.EgressProxyURL)
	}
}

// TestM8MCPJungleEnvVarsOverride — env-var overrides land in the
// config struct verbatim.
func TestM8MCPJungleEnvVarsOverride(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_MCPJUNGLE_URL", "http://garrison-mcpjungle:8080")
	t.Setenv("GARRISON_MCPJUNGLE_ADMIN_TOKEN_PATH", "ops/mcpjungle/admin")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MCPJungleURL != "http://garrison-mcpjungle:8080" {
		t.Errorf("MCPJungleURL = %q; want override", cfg.MCPJungleURL)
	}
	if cfg.MCPJungleAdminTokenPath != "ops/mcpjungle/admin" {
		t.Errorf("MCPJungleAdminTokenPath = %q; want override", cfg.MCPJungleAdminTokenPath)
	}
}

// TestConfigSchedDefaults — with no M9 env vars set, the schedule knobs
// carry their documented defaults: tick 30s, min interval 15m, per-turn
// creation ceiling 3 (M9 plan decision 12).
func TestConfigSchedDefaults(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SchedTickInterval != 30*time.Second {
		t.Errorf("SchedTickInterval = %v; want 30s", cfg.SchedTickInterval)
	}
	if cfg.SchedMinInterval != 15*time.Minute {
		t.Errorf("SchedMinInterval = %v; want 15m", cfg.SchedMinInterval)
	}
	if cfg.MaxScheduledTasksPerTurn != 3 {
		t.Errorf("MaxScheduledTasksPerTurn = %d; want 3", cfg.MaxScheduledTasksPerTurn)
	}
}

// TestConfigSchedOverrides — every M9 env var is honoured when set to a
// valid value.
func TestConfigSchedOverrides(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_SCHED_TICK_INTERVAL", "10s")
	t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "5m")
	t.Setenv("GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN", "7")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SchedTickInterval != 10*time.Second {
		t.Errorf("SchedTickInterval = %v; want 10s", cfg.SchedTickInterval)
	}
	if cfg.SchedMinInterval != 5*time.Minute {
		t.Errorf("SchedMinInterval = %v; want 5m", cfg.SchedMinInterval)
	}
	if cfg.MaxScheduledTasksPerTurn != 7 {
		t.Errorf("MaxScheduledTasksPerTurn = %d; want 7", cfg.MaxScheduledTasksPerTurn)
	}
}

// TestConfigSchedRejectsZeroTick — tick intervals below the 1s floor are
// rejected at startup ("0s", "500ms", and negatives all fail Load).
func TestConfigSchedRejectsZeroTick(t *testing.T) {
	for _, v := range []string{"0s", "500ms", "-5s"} {
		t.Run("v="+v, func(t *testing.T) {
			clearAll(t)
			t.Setenv("GARRISON_DATABASE_URL", validDBURL)
			t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
			t.Setenv("GARRISON_SCHED_TICK_INTERVAL", v)
			if _, err := config.Load(); err == nil {
				t.Errorf("expected error for tick interval %q", v)
			}
		})
	}
}

// -------- M10 T003 ingress env-var tests ------------------------------------

// TestConfigIngressDefaults — with no M10 env vars set, the config carries the
// documented defaults: port 8082, GitHub connector disabled, connector ID
// "github-sortie", rate 60/min, burst 30 (plan.md §Configuration + vault,
// decision 9; tasks.md T003 completion condition).
func TestConfigIngressDefaults(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.IngressPort != config.DefaultIngressPort {
		t.Errorf("IngressPort = %d; want %d", cfg.IngressPort, config.DefaultIngressPort)
	}
	if cfg.IngressGitHubEnabled {
		t.Error("IngressGitHubEnabled = true; want false (disabled by default)")
	}
	if cfg.IngressGitHubConnectorID != config.DefaultIngressGitHubConnectorID {
		t.Errorf("IngressGitHubConnectorID = %q; want %q", cfg.IngressGitHubConnectorID, config.DefaultIngressGitHubConnectorID)
	}
	if cfg.IngressGitHubDepartment != "" {
		t.Errorf("IngressGitHubDepartment = %q; want empty (unset by default)", cfg.IngressGitHubDepartment)
	}
	if cfg.IngressGitHubRatePerMin != config.DefaultIngressGitHubRatePerMin {
		t.Errorf("IngressGitHubRatePerMin = %d; want %d", cfg.IngressGitHubRatePerMin, config.DefaultIngressGitHubRatePerMin)
	}
	if cfg.IngressGitHubBurst != config.DefaultIngressGitHubBurst {
		t.Errorf("IngressGitHubBurst = %d; want %d", cfg.IngressGitHubBurst, config.DefaultIngressGitHubBurst)
	}
}

// TestConfigIngressOverrides — every M10 ingress env var is honoured when set
// to a valid value (tasks.md T003 completion condition).
func TestConfigIngressOverrides(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_INGRESS_PORT", "9082")
	t.Setenv("GARRISON_INGRESS_GITHUB_ENABLED", "true")
	t.Setenv("GARRISON_INGRESS_GITHUB_CONNECTOR_ID", "github-myorg")
	t.Setenv("GARRISON_INGRESS_GITHUB_DEPARTMENT", "engineering")
	t.Setenv("GARRISON_INGRESS_GITHUB_RATE_PER_MIN", "120")
	t.Setenv("GARRISON_INGRESS_GITHUB_BURST", "60")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.IngressPort != 9082 {
		t.Errorf("IngressPort = %d; want 9082", cfg.IngressPort)
	}
	if !cfg.IngressGitHubEnabled {
		t.Error("IngressGitHubEnabled = false; want true")
	}
	if cfg.IngressGitHubConnectorID != "github-myorg" {
		t.Errorf("IngressGitHubConnectorID = %q; want github-myorg", cfg.IngressGitHubConnectorID)
	}
	if cfg.IngressGitHubDepartment != "engineering" {
		t.Errorf("IngressGitHubDepartment = %q; want engineering", cfg.IngressGitHubDepartment)
	}
	if cfg.IngressGitHubRatePerMin != 120 {
		t.Errorf("IngressGitHubRatePerMin = %d; want 120", cfg.IngressGitHubRatePerMin)
	}
	if cfg.IngressGitHubBurst != 60 {
		t.Errorf("IngressGitHubBurst = %d; want 60", cfg.IngressGitHubBurst)
	}
}

// TestConfigIngressRejectsZeroPort — GARRISON_INGRESS_PORT=0 is rejected at
// startup (tasks.md T003 completion condition; FR-302 posture).
func TestConfigIngressRejectsZeroPort(t *testing.T) {
	for _, v := range []string{"0", "-1", "-8082"} {
		t.Run("port="+v, func(t *testing.T) {
			clearAll(t)
			t.Setenv("GARRISON_DATABASE_URL", validDBURL)
			t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
			t.Setenv("GARRISON_INGRESS_PORT", v)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load(%q): want error for invalid port, got nil", v)
			}
			if !strings.Contains(err.Error(), "GARRISON_INGRESS_PORT") {
				t.Errorf("error = %v; want it to name GARRISON_INGRESS_PORT", err)
			}
		})
	}
}

// TestConfigIngressRejectsEmptyDepartmentWhenEnabled — when
// GARRISON_INGRESS_GITHUB_ENABLED=true but GARRISON_INGRESS_GITHUB_DEPARTMENT
// is unset/empty, Load returns an error (required field; tasks.md T003
// completion condition; fail-closed per FR-302 posture).
func TestConfigIngressRejectsEmptyDepartmentWhenEnabled(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_INGRESS_GITHUB_ENABLED", "true")
	// GARRISON_INGRESS_GITHUB_DEPARTMENT intentionally left unset.

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error when GARRISON_INGRESS_GITHUB_ENABLED=true without DEPARTMENT, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_INGRESS_GITHUB_DEPARTMENT") {
		t.Errorf("error = %v; want it to name GARRISON_INGRESS_GITHUB_DEPARTMENT", err)
	}
}

// TestConfigSchedRejectsZeroCeiling — a per-turn creation ceiling below 1
// is rejected; also covers the min-interval <= 0 rejection in the same
// startup-validation family.
func TestConfigSchedRejectsZeroCeiling(t *testing.T) {
	for _, v := range []string{"0", "-1"} {
		t.Run("ceiling="+v, func(t *testing.T) {
			clearAll(t)
			t.Setenv("GARRISON_DATABASE_URL", validDBURL)
			t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
			t.Setenv("GARRISON_CHAT_MAX_SCHEDULED_TASKS_PER_TURN", v)
			if _, err := config.Load(); err == nil {
				t.Errorf("expected error for ceiling %q", v)
			}
		})
	}
	t.Run("minInterval=0s", func(t *testing.T) {
		clearAll(t)
		t.Setenv("GARRISON_DATABASE_URL", validDBURL)
		t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
		t.Setenv("GARRISON_SCHED_MIN_INTERVAL", "0s")
		if _, err := config.Load(); err == nil {
			t.Error("expected error for min interval 0s")
		}
	})
}

// -------- M11 T003 action-broker env-var tests --------------------------------

// TestConfigActionDefaults — with no M11 action-broker env vars set, the
// config carries the documented defaults: PAT path "actions/GITHUB_PAT",
// poll interval 30s (tasks.md T003 completion condition).
func TestConfigActionDefaults(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ActionGitHubPATPath != config.DefaultActionGitHubPATPath {
		t.Errorf("ActionGitHubPATPath = %q; want %q", cfg.ActionGitHubPATPath, config.DefaultActionGitHubPATPath)
	}
	if cfg.ActionPollInterval != config.DefaultActionPollInterval {
		t.Errorf("ActionPollInterval = %v; want %v", cfg.ActionPollInterval, config.DefaultActionPollInterval)
	}
}

// TestConfigActionOverrides — env-var overrides land in the config struct
// verbatim (tasks.md T003 completion condition).
func TestConfigActionOverrides(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	t.Setenv("GARRISON_ACTION_GITHUB_PAT_PATH", "ops/github/PAT")
	t.Setenv("GARRISON_ACTION_POLL_INTERVAL", "15s")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ActionGitHubPATPath != "ops/github/PAT" {
		t.Errorf("ActionGitHubPATPath = %q; want ops/github/PAT", cfg.ActionGitHubPATPath)
	}
	if cfg.ActionPollInterval != 15*time.Second {
		t.Errorf("ActionPollInterval = %v; want 15s", cfg.ActionPollInterval)
	}
}

// TestConfigActionRejectsEmptyPATPath — setting GARRISON_ACTION_GITHUB_PAT_PATH
// to a whitespace-only string is rejected at startup (tasks.md T003 completion
// condition; fail-closed posture — an empty path means no credential is
// configured). The implementation trims the value and rejects the empty result.
// A whitespace-only value passes the `v != ""` env-override guard but becomes
// empty after TrimSpace, triggering the reject-empty validation.
func TestConfigActionRejectsEmptyPATPath(t *testing.T) {
	clearAll(t)
	t.Setenv("GARRISON_DATABASE_URL", validDBURL)
	t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
	// A whitespace-only string passes the `v != ""` env-override guard but is
	// trimmed to "" and then rejected by the non-empty guard.
	t.Setenv("GARRISON_ACTION_GITHUB_PAT_PATH", "   ")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load(): want error for whitespace-only GARRISON_ACTION_GITHUB_PAT_PATH, got nil")
	}
	if !strings.Contains(err.Error(), "GARRISON_ACTION_GITHUB_PAT_PATH") {
		t.Errorf("error = %v; want it to name GARRISON_ACTION_GITHUB_PAT_PATH", err)
	}
}

// TestConfigActionRejectsTooShortPollInterval — GARRISON_ACTION_POLL_INTERVAL
// values below 1s are rejected at startup (tasks.md T003 completion condition;
// mirrors the GARRISON_POLL_INTERVAL 1s floor in parseDurationWithMin).
func TestConfigActionRejectsTooShortPollInterval(t *testing.T) {
	for _, v := range []string{"0s", "500ms", "-5s"} {
		t.Run("interval="+v, func(t *testing.T) {
			clearAll(t)
			t.Setenv("GARRISON_DATABASE_URL", validDBURL)
			t.Setenv("GARRISON_FAKE_AGENT_CMD", validFakeCmd)
			t.Setenv("GARRISON_ACTION_POLL_INTERVAL", v)

			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load(%q): want error for too-short poll interval, got nil", v)
			}
			if !strings.Contains(err.Error(), "GARRISON_ACTION_POLL_INTERVAL") {
				t.Errorf("error = %v; want it to name GARRISON_ACTION_POLL_INTERVAL", err)
			}
		})
	}
}
