// Package mcpconfig writes and removes per-invocation MCP config files for
// Claude Code spawns. One file per agent_instance.id, named
// mcp-config-<uuid>.json under the supervisor-owned state directory
// (GARRISON_MCP_CONFIG_DIR, default /var/lib/garrison/mcp/). The file is
// created before cmd.Start() and removed on subprocess exit; the short
// lifetime is what keeps the read-only DSN out of any long-lived on-disk
// state per NFR-111.
//
// The JSON schema carries exactly one MCP server entry in M2.1 (postgres,
// read-only, pointing at the supervisor's mcp-postgres subcommand with the
// DSN in env, not argv — so it never appears in `ps`). The `mcpServers`
// shape is an object map, so adding MemPalace in M2.2 is additive: one
// more key, no structural change (plan §"Deferred to later milestones").
//
// The package exposes an internal seam (fileWriter) so disk-full / perm-
// denied behaviour can be exercised in unit tests without requiring a
// real full-disk or chmod'd directory.
package mcpconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrVaultMCPBanned is returned by RejectVaultServers (and surfaced by Write)
// when the composed config contains a banned vault-pattern MCP server name.
// spawn.go checks for this sentinel to route to ExitVaultMCPInConfig.
var ErrVaultMCPBanned = errors.New("mcpconfig: vault-pattern server banned")

// bannedMCPNamePatterns lists substring patterns that flag a vault-proxying
// MCP server in the config (Rule 3 / FR-410 / D2.4). Case-insensitive match.
var bannedMCPNamePatterns = []string{"vault", "secret", "infisical"}

// RejectVaultServers checks the composed MCP config for any server whose name
// matches a banned pattern. Returns a non-nil error naming the offending key
// on the first match; nil means no banned server found.
// Called from WriteWithOps before the config is written to disk.
func RejectVaultServers(cfg mcpConfig) error {
	for name := range cfg.MCPServers {
		lower := strings.ToLower(name)
		for _, pattern := range bannedMCPNamePatterns {
			if strings.Contains(lower, pattern) {
				return fmt.Errorf("%w: server %q matched pattern %q", ErrVaultMCPBanned, name, pattern)
			}
		}
	}
	return nil
}

// mcpConfig is the top-level shape Claude Code expects for --mcp-config.
type mcpConfig struct {
	MCPServers map[string]mcpServerSpec `json:"mcpServers"`
}

// mcpServerSpec is one entry under mcpServers. The command is an absolute
// path; args is the argv beyond argv[0]; env contains the supervisor-
// authored read-only DSN for the Postgres MCP subprocess.
type mcpServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// fileOps is the I/O seam. Production uses osOps (os.WriteFile,
// os.Remove); tests substitute a stub that can inject ENOSPC etc.
type fileOps interface {
	WriteFile(name string, data []byte, perm fs.FileMode) error
	Remove(name string) error
}

type osOps struct{}

func (osOps) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (osOps) Remove(name string) error { return os.Remove(name) }

// DefaultOps is the production os-backed fileOps. Kept unexported-ish
// (exported so tests in other packages could wire it, but primarily for
// internal use).
var DefaultOps fileOps = osOps{}

// MempalaceParams bundles the four values the mempalace MCP entry needs.
// Passing a struct keeps Write's signature from ballooning. Empty values
// suppress the mempalace entry entirely (useful for M2.1-era tests that
// exercise a postgres-only shape).
type MempalaceParams struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string
}

func (mp MempalaceParams) enabled() bool {
	return mp.DockerBin != "" && mp.MempalaceContainer != "" && mp.PalacePath != ""
}

// FinalizeParams bundles the values the finalize MCP entry needs
// (M2.2.1 FR-256). SupervisorBin resolves the supervisor binary path;
// AgentInstanceID is the uuid the supervisor just INSERTed in the
// spawn.Spawn dedupe transaction — the finalize subcommand reads it
// from GARRISON_AGENT_INSTANCE_ID env per spec Clarification 2026-04-23 Q4;
// DatabaseURL is the garrison_agent_ro DSN the finalize handler uses
// to query agent_instances for the already-committed check (FR-260).
// Leaving AgentInstanceID empty disables the entry (useful for M2.1/M2.2-
// era tests that don't exercise finalize).
type FinalizeParams struct {
	SupervisorBin   string
	AgentInstanceID string
	DatabaseURL     string
}

func (fp FinalizeParams) enabled() bool {
	return fp.SupervisorBin != "" && fp.AgentInstanceID != "" && fp.DatabaseURL != ""
}

// Write creates the per-invocation MCP config file. Returns the absolute
// path on success. The caller is expected to `defer Remove(path)` against
// the returned path so every exit path (success, claude error, timeout,
// supervisor shutdown, spawn-failed) converges on the same cleanup.
//
// File permission is 0o600: the file contains a read-only DSN. The
// parent directory's ownership and mode are the operator's responsibility
// (plan §NFR-105 — 0o750 root-owned is the documented default). A
// write failure is surfaced verbatim so the caller can distinguish
// ENOSPC from permission-denied and emit a matching exit_reason
// (FR-103 + clarify Q2 → exit_reason="spawn_failed", dispatcher continues).
//
// M2.2 extension (T011): if mempalace is non-zero, a second `mempalace`
// entry is added to mcpServers per plan §"MCP config extension" /
// internal/mempalace.MCPServerSpec. FR-204 / FR-205 (additive; postgres
// entry untouched).
func Write(ctx context.Context, dir string, instanceID pgtype.UUID, supervisorBin, dsn string, mempalaceParams MempalaceParams, finalizeParams FinalizeParams) (string, error) {
	return WriteWithOps(ctx, DefaultOps, dir, instanceID, supervisorBin, dsn, mempalaceParams, finalizeParams)
}

// WriteWithOps is the testable form. Production callers use Write.
func WriteWithOps(_ context.Context, ops fileOps, dir string, instanceID pgtype.UUID, supervisorBin, dsn string, mempalaceParams MempalaceParams, finalizeParams FinalizeParams) (string, error) {
	if dir == "" {
		return "", errors.New("mcpconfig: dir is empty")
	}
	if supervisorBin == "" {
		return "", errors.New("mcpconfig: supervisorBin is empty")
	}
	// Canonicalize the UUID to its textual form for the filename.
	idText, err := formatUUID(instanceID)
	if err != nil {
		return "", fmt.Errorf("mcpconfig: format instanceID: %w", err)
	}
	path := filepath.Join(dir, "mcp-config-"+idText+".json")

	servers := map[string]mcpServerSpec{
		"postgres": {
			Command: supervisorBin,
			Args:    []string{"mcp-postgres"},
			Env:     map[string]string{"GARRISON_PGMCP_DSN": dsn},
		},
	}
	if mempalaceParams.enabled() {
		command, args, env := mempalace.MCPServerSpec(mempalace.SpecConfig{
			DockerBin:          mempalaceParams.DockerBin,
			MempalaceContainer: mempalaceParams.MempalaceContainer,
			PalacePath:         mempalaceParams.PalacePath,
			DockerHost:         mempalaceParams.DockerHost,
		})
		servers["mempalace"] = mcpServerSpec{
			Command: command,
			Args:    args,
			Env:     env,
		}
	}
	// M2.2.1 T004: third entry `finalize` — the in-tree MCP server
	// implementing the finalize_ticket tool. Env carries the agent_
	// instance_id per spec Clarification 2026-04-23 Q4 so the server
	// can scope its already-committed check and attempt reporting to
	// this specific spawn. GARRISON_DATABASE_URL lets the handler run
	// SelectAgentInstanceFinalizedState on every tool call (FR-260).
	if finalizeParams.enabled() {
		servers["finalize"] = mcpServerSpec{
			Command: finalizeParams.SupervisorBin,
			Args:    []string{"mcp", "finalize"},
			Env: map[string]string{
				"GARRISON_AGENT_INSTANCE_ID": finalizeParams.AgentInstanceID,
				"GARRISON_DATABASE_URL":      finalizeParams.DatabaseURL,
			},
		}
	}

	cfg := mcpConfig{MCPServers: servers}

	// Rule 3 (FR-410): reject before serialising so no vault-proxying config
	// ever reaches disk. The error is classified into ExitVaultMCPInConfig
	// by the spawn path (T008).
	if err := RejectVaultServers(cfg); err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		// encoding/json never errors for this shape, but surface it
		// anyway so the caller isn't surprised on a future schema bump.
		return "", fmt.Errorf("mcpconfig: marshal: %w", err)
	}

	if err := ops.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("mcpconfig: write %s: %w", path, err)
	}
	return path, nil
}

// Remove deletes the per-invocation config file. Tolerant of
// os.IsNotExist so a double-remove (e.g. if an external cleanup ran) is
// a silent no-op rather than an error — the invariant the caller needs
// is "the file is gone", which is satisfied either way.
func Remove(path string) error { return RemoveWithOps(DefaultOps, path) }

// RemoveWithOps is the testable form. Production callers use Remove.
func RemoveWithOps(ops fileOps, path string) error {
	if err := ops.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("mcpconfig: remove %s: %w", path, err)
	}
	return nil
}

// formatUUID stringifies a pgtype.UUID into canonical 8-4-4-4-12 form.
// Defined locally so mcpconfig doesn't import internal/spawn's helper.
func formatUUID(u pgtype.UUID) (string, error) {
	b, err := u.Value()
	if err != nil {
		return "", err
	}
	s, ok := b.(string)
	if !ok {
		return "", fmt.Errorf("expected string value, got %T", b)
	}
	return s, nil
}
