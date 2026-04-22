package config_test

import (
	"log/slog"
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
