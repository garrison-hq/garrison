// Package config loads and validates the supervisor's environment-variable
// configuration into a typed Config struct. Defaults and bounds are pinned
// to the requirements cited in each constant's comment.
package config

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"time"
)

const (
	// DefaultPollInterval is the fallback poll cadence (plan.md §"Config", NFR-001).
	DefaultPollInterval = 5 * time.Second
	// MinPollInterval is the floor enforced by Load per NFR-001.
	MinPollInterval = 1 * time.Second
	// DefaultSubprocessTimeout bounds a single agent subprocess (NFR-003).
	DefaultSubprocessTimeout = 60 * time.Second
	// DefaultShutdownGrace is the total graceful-shutdown budget (NFR-004).
	DefaultShutdownGrace = 30 * time.Second
	// DefaultHealthPort is the bind port for /health (FR-016).
	DefaultHealthPort uint16 = 8080
	// DefaultLogLevel matches plan.md §"Config" (NFR-008).
	DefaultLogLevel = slog.LevelInfo
)

// Config mirrors the seven env vars in plan.md §"Config" one-to-one using
// Go-native types.
type Config struct {
	DatabaseURL       string
	FakeAgentCmd      string
	PollInterval      time.Duration
	SubprocessTimeout time.Duration
	ShutdownGrace     time.Duration
	HealthPort        uint16
	LogLevel          slog.Level
}

// Load reads ORG_OS_* env vars, applies defaults, and validates per plan.md
// §"Config". It returns a descriptive error that names the offending env var
// on any failure.
func Load() (*Config, error) {
	cfg := &Config{
		PollInterval:      DefaultPollInterval,
		SubprocessTimeout: DefaultSubprocessTimeout,
		ShutdownGrace:     DefaultShutdownGrace,
		HealthPort:        DefaultHealthPort,
		LogLevel:          DefaultLogLevel,
	}

	dbURL := os.Getenv("ORG_OS_DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("config: required env var ORG_OS_DATABASE_URL is unset or empty")
	}
	if _, err := url.Parse(dbURL); err != nil {
		return nil, fmt.Errorf("config: ORG_OS_DATABASE_URL is not a parseable URL: %w", err)
	}
	cfg.DatabaseURL = dbURL

	fakeCmd := os.Getenv("ORG_OS_FAKE_AGENT_CMD")
	if fakeCmd == "" {
		return nil, fmt.Errorf("config: required env var ORG_OS_FAKE_AGENT_CMD is unset or empty")
	}
	cfg.FakeAgentCmd = fakeCmd

	if v := os.Getenv("ORG_OS_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: ORG_OS_POLL_INTERVAL %q is not a valid duration: %w", v, err)
		}
		if d < MinPollInterval {
			return nil, fmt.Errorf("config: ORG_OS_POLL_INTERVAL (%s) must be >= %s", d, MinPollInterval)
		}
		cfg.PollInterval = d
	}

	if v := os.Getenv("ORG_OS_SUBPROCESS_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: ORG_OS_SUBPROCESS_TIMEOUT %q is not a valid duration: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: ORG_OS_SUBPROCESS_TIMEOUT must be positive (got %s)", d)
		}
		cfg.SubprocessTimeout = d
	}

	if v := os.Getenv("ORG_OS_SHUTDOWN_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: ORG_OS_SHUTDOWN_GRACE %q is not a valid duration: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: ORG_OS_SHUTDOWN_GRACE must be positive (got %s)", d)
		}
		cfg.ShutdownGrace = d
	}

	if v := os.Getenv("ORG_OS_HEALTH_PORT"); v != "" {
		n, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("config: ORG_OS_HEALTH_PORT %q is not a valid uint16: %w", v, err)
		}
		if n == 0 {
			return nil, fmt.Errorf("config: ORG_OS_HEALTH_PORT must be non-zero")
		}
		cfg.HealthPort = uint16(n)
	}

	if v := os.Getenv("ORG_OS_LOG_LEVEL"); v != "" {
		level, err := parseLogLevel(v)
		if err != nil {
			return nil, err
		}
		cfg.LogLevel = level
	}

	return cfg, nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: ORG_OS_LOG_LEVEL %q is not one of debug|info|warn|error", s)
	}
}
