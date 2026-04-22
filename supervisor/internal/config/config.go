// Package config loads and validates the supervisor's environment-variable
// configuration into a typed Config struct. Defaults and bounds are pinned
// to the requirements cited in each constant's comment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const (
	// DefaultPollInterval is the fallback poll cadence (plan.md §"Config", NFR-001).
	DefaultPollInterval = 5 * time.Second
	// MinPollInterval is the floor enforced by Load per NFR-001.
	MinPollInterval = 1 * time.Second
	// DefaultSubprocessTimeout bounds a single agent subprocess (NFR-003, NFR-101).
	// M2.1: timing runs from cmd.Start() (clarify-session Q1) — this is exactly
	// how exec.CommandContext already treats context deadlines.
	DefaultSubprocessTimeout = 60 * time.Second
	// DefaultShutdownGrace is the total graceful-shutdown budget (NFR-004).
	DefaultShutdownGrace = 30 * time.Second
	// DefaultHealthPort is the bind port for /health (FR-016).
	DefaultHealthPort uint16 = 8080
	// DefaultLogLevel matches plan.md §"Config" (NFR-008).
	DefaultLogLevel = slog.LevelInfo
	// DefaultClaudeBudgetUSD is the per-invocation hard ceiling passed to Claude
	// as --max-budget-usd (plan.md §"internal/config").
	DefaultClaudeBudgetUSD = 0.05
	// DefaultMCPConfigDir matches the Dockerfile runtime stage
	// (plan.md §"internal/config", §"Dockerfile + claude binary install").
	DefaultMCPConfigDir = "/var/lib/garrison/mcp/"
	// AgentRORole is the Postgres login role that MCP-initiated queries use.
	// The read-only grant set is established in the M2.1 migration (NFR-104).
	AgentRORole = "garrison_agent_ro"
)

// Config mirrors the env vars in plan.md §"Config" one-to-one using
// Go-native types. Fields introduced by M2.1 are grouped below the M1 block.
type Config struct {
	// M1 fields
	DatabaseURL       string
	FakeAgentCmd      string
	PollInterval      time.Duration
	SubprocessTimeout time.Duration
	ShutdownGrace     time.Duration
	HealthPort        uint16
	LogLevel          slog.Level

	// M2.1 fields (plan.md §"internal/config")
	ClaudeBin       string
	ClaudeModel     string
	ClaudeBudgetUSD float64
	MCPConfigDir    string
	UseFakeAgent    bool

	agentROPassword string
}

// AgentRODSN returns the read-only DSN handed to the in-tree MCP server. It is
// derived from DatabaseURL with the userinfo replaced by garrison_agent_ro plus
// the configured password. The raw password is kept unexported; callers only
// ever see the composed DSN.
func (c *Config) AgentRODSN() string {
	if c.DatabaseURL == "" {
		return ""
	}
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return ""
	}
	u.User = url.UserPassword(AgentRORole, c.agentROPassword)
	return u.String()
}

// Load reads GARRISON_* env vars, applies defaults, and validates per plan.md
// §"Config" and §"internal/config". It returns a descriptive error that names
// the offending env var on any failure.
func Load() (*Config, error) {
	cfg := &Config{
		PollInterval:      DefaultPollInterval,
		SubprocessTimeout: DefaultSubprocessTimeout,
		ShutdownGrace:     DefaultShutdownGrace,
		HealthPort:        DefaultHealthPort,
		LogLevel:          DefaultLogLevel,
		ClaudeBudgetUSD:   DefaultClaudeBudgetUSD,
		MCPConfigDir:      DefaultMCPConfigDir,
	}

	dbURL := os.Getenv("GARRISON_DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("config: required env var GARRISON_DATABASE_URL is unset or empty")
	}
	if _, err := url.Parse(dbURL); err != nil {
		return nil, fmt.Errorf("config: GARRISON_DATABASE_URL is not a parseable URL: %w", err)
	}
	cfg.DatabaseURL = dbURL

	// The fake-agent escape hatch stays available for M1 integration tests.
	// Setting GARRISON_FAKE_AGENT_CMD flips UseFakeAgent=true and suppresses
	// the M2.1-only validations (claude binary resolution, MCP config dir
	// writability, read-only password requirement) so M1 tests keep working
	// without needing a real claude install.
	if fakeCmd := os.Getenv("GARRISON_FAKE_AGENT_CMD"); fakeCmd != "" {
		cfg.FakeAgentCmd = fakeCmd
		cfg.UseFakeAgent = true
	}

	if v := os.Getenv("GARRISON_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: GARRISON_POLL_INTERVAL %q is not a valid duration: %w", v, err)
		}
		if d < MinPollInterval {
			return nil, fmt.Errorf("config: GARRISON_POLL_INTERVAL (%s) must be >= %s", d, MinPollInterval)
		}
		cfg.PollInterval = d
	}

	if v := os.Getenv("GARRISON_SUBPROCESS_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: GARRISON_SUBPROCESS_TIMEOUT %q is not a valid duration: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: GARRISON_SUBPROCESS_TIMEOUT must be positive (got %s)", d)
		}
		cfg.SubprocessTimeout = d
	}

	if v := os.Getenv("GARRISON_SHUTDOWN_GRACE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("config: GARRISON_SHUTDOWN_GRACE %q is not a valid duration: %w", v, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("config: GARRISON_SHUTDOWN_GRACE must be positive (got %s)", d)
		}
		cfg.ShutdownGrace = d
	}

	if v := os.Getenv("GARRISON_HEALTH_PORT"); v != "" {
		n, err := strconv.ParseUint(v, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("config: GARRISON_HEALTH_PORT %q is not a valid uint16: %w", v, err)
		}
		if n == 0 {
			return nil, fmt.Errorf("config: GARRISON_HEALTH_PORT must be non-zero")
		}
		cfg.HealthPort = uint16(n)
	}

	if v := os.Getenv("GARRISON_LOG_LEVEL"); v != "" {
		level, err := parseLogLevel(v)
		if err != nil {
			return nil, err
		}
		cfg.LogLevel = level
	}

	cfg.ClaudeModel = os.Getenv("GARRISON_CLAUDE_MODEL")

	if v := os.Getenv("GARRISON_CLAUDE_BUDGET_USD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("config: GARRISON_CLAUDE_BUDGET_USD %q is not a valid number: %w", v, err)
		}
		if f <= 0 || f >= 1.0 {
			return nil, fmt.Errorf("config: GARRISON_CLAUDE_BUDGET_USD (%v) must be in the open interval (0, 1)", f)
		}
		cfg.ClaudeBudgetUSD = f
	}

	if v := os.Getenv("GARRISON_MCP_CONFIG_DIR"); v != "" {
		cfg.MCPConfigDir = v
	}

	// From here on we enforce M2.1 preconditions that only matter to the
	// real-Claude path. The fake-agent escape hatch skips them wholesale.
	if cfg.UseFakeAgent {
		return cfg, nil
	}

	pw := os.Getenv("GARRISON_AGENT_RO_PASSWORD")
	if pw == "" {
		return nil, fmt.Errorf("config: required env var GARRISON_AGENT_RO_PASSWORD is unset or empty")
	}
	cfg.agentROPassword = pw

	if err := ensureWritableDir(cfg.MCPConfigDir); err != nil {
		return nil, fmt.Errorf("config: GARRISON_MCP_CONFIG_DIR %q: %w", cfg.MCPConfigDir, err)
	}

	bin, err := resolveClaudeBin(os.Getenv("GARRISON_CLAUDE_BIN"))
	if err != nil {
		return nil, err
	}
	cfg.ClaudeBin = bin

	return cfg, nil
}

// resolveClaudeBin honours an explicit GARRISON_CLAUDE_BIN override and
// otherwise falls back to $PATH lookup. The error message is exact per
// plan.md §"internal/config" so operators see the same string in every env.
func resolveClaudeBin(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return "", errors.New("config: cannot find claude binary on $PATH and GARRISON_CLAUDE_BIN is unset")
	}
	return bin, nil
}

// ensureWritableDir creates dir (and parents) if needed, then verifies the
// supervisor can actually write to it by touching a sentinel file. The
// probe catches read-only mounts and permission mismatches that MkdirAll
// alone would happily miss.
func ensureWritableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".garrison-writable-probe-*")
	if err != nil {
		return fmt.Errorf("write probe: %w", err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	// filepath.Clean just to flag unusual inputs in the probe name — not
	// strictly needed but keeps the error path tidy if anything goes wrong.
	_ = filepath.Clean(dir)
	return nil
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
		return 0, fmt.Errorf("config: GARRISON_LOG_LEVEL %q is not one of debug|info|warn|error", s)
	}
}
