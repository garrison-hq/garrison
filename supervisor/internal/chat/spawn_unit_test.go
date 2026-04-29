package chat

import (
	"errors"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestDockerRunArgs_DefaultsAreApplied: deps.DefaultModel="" and
// deps.DockerNetwork="" must fall back to the documented defaults so
// a partially-configured environment still launches a working chat
// container. The values themselves are part of the operator contract
// (ops-checklist references them); changing either requires updating
// docs/ops-checklist.md and this test together.
func TestDockerRunArgs_DefaultsAreApplied(t *testing.T) {
	args := dockerRunArgs(Deps{ChatContainerImage: "garrison-claude:m5"}, "/tmp/mcp.json", "CLAUDE_CODE_OAUTH_TOKEN=tok", "", "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--model claude-sonnet-4-6") {
		t.Errorf("expected default model claude-sonnet-4-6 in argv; got %v", args)
	}
	if !strings.Contains(joined, "--network garrison-net") {
		t.Errorf("expected default network garrison-net in argv; got %v", args)
	}
}

// TestDockerRunArgs_OperatorOverridesWin: when deps carries explicit
// values they should be passed through verbatim. Pin both the model
// and network knobs the ops-checklist exposes via GARRISON_CHAT_*.
func TestDockerRunArgs_OperatorOverridesWin(t *testing.T) {
	args := dockerRunArgs(Deps{
		ChatContainerImage: "garrison-claude:custom",
		DefaultModel:       "claude-opus-4-7",
		DockerNetwork:      "operator-net",
	}, "/tmp/mcp.json", "CLAUDE_CODE_OAUTH_TOKEN=tok", "/host/supervisor", "/host/docker")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--model claude-opus-4-7") {
		t.Errorf("expected operator-supplied model in argv; got %v", args)
	}
	if !strings.Contains(joined, "--network operator-net") {
		t.Errorf("expected operator-supplied network in argv; got %v", args)
	}
	if !strings.Contains(joined, "garrison-claude:custom") {
		t.Errorf("expected custom image in argv; got %v", args)
	}
	// M5.2 follow-up: supervisor + docker CLI must be bind-mounted into
	// the chat container at the host-side paths the MCP config writes,
	// otherwise claude inside the container can't spawn either MCP
	// server (postgres needs supervisorBin; mempalace needs dockerBin).
	if !strings.Contains(joined, "/host/supervisor:/host/supervisor:ro") {
		t.Errorf("expected supervisor bind mount in argv; got %v", args)
	}
	if !strings.Contains(joined, "/host/docker:/host/docker:ro") {
		t.Errorf("expected docker CLI bind mount in argv; got %v", args)
	}
}

// TestDockerRunArgs_NoMountWhenPathsEmpty: empty supervisorBin /
// dockerBin must NOT add any -v flags (test-mode + ops-checklist
// "minimal config" path). Without this skip, an empty path would
// produce an invalid `-v :` flag and docker would refuse to start.
func TestDockerRunArgs_NoMountWhenPathsEmpty(t *testing.T) {
	args := dockerRunArgs(Deps{ChatContainerImage: "garrison-claude:m5"}, "/tmp/mcp.json", "T=t", "", "")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, ":/usr/local/bin/supervisor") {
		t.Errorf("empty supervisorBin must not produce a mount; got %v", args)
	}
	if strings.Contains(joined, ":/usr/bin/docker") {
		t.Errorf("empty dockerBin must not produce a mount; got %v", args)
	}
}

// TestDockerRunArgs_StrictMCPConfigAlwaysSet: --strict-mcp-config is
// load-bearing (FR-022 / Q-E: chat MUST run with the supervisor-
// supplied MCP file as the sole source of MCP servers, no inheritance
// from ~/.claude/mcp.json). A regression that drops the flag would
// silently allow the operator's local MCP config to leak in.
func TestDockerRunArgs_StrictMCPConfigAlwaysSet(t *testing.T) {
	args := dockerRunArgs(Deps{ChatContainerImage: "img"}, "/tmp/mcp.json", "T=t", "", "")
	if !contains(args, "--strict-mcp-config") {
		t.Errorf("argv missing --strict-mcp-config: %v", args)
	}
	if !contains(args, "--mcp-config") {
		t.Errorf("argv missing --mcp-config: %v", args)
	}
}

// TestComposeFullVaultPath_InvalidCustomerID: when customer_id is
// invalid (zero pgtype.UUID), the suffix is returned as-is — used by
// the M2.3 vault-path composition for tenant-less paths.
func TestComposeFullVaultPath_InvalidCustomerID(t *testing.T) {
	got := composeFullVaultPath(pgtype.UUID{}, "/foo/bar")
	if got != "/foo/bar" {
		t.Errorf("got %q; want %q", got, "/foo/bar")
	}
}

// TestComposeFullVaultPath_EmptySuffix: customer_id alone produces
// "/<uuid>" — the path the vault uses for tenant-root secrets.
func TestComposeFullVaultPath_EmptySuffix(t *testing.T) {
	var u pgtype.UUID
	if err := u.Scan("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := composeFullVaultPath(u, "")
	if got != "/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
		t.Errorf("got %q", got)
	}
}

// TestComposeFullVaultPath_LeadingSlashSuffix: when the suffix already
// starts with "/", the helper joins without doubling it. This is the
// hot-path shape the M5.1 chat OAuth-token fetch uses
// (suffix="/operator/CLAUDE_CODE_OAUTH_TOKEN").
func TestComposeFullVaultPath_LeadingSlashSuffix(t *testing.T) {
	var u pgtype.UUID
	if err := u.Scan("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := composeFullVaultPath(u, "/operator/CLAUDE_CODE_OAUTH_TOKEN")
	want := "/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee/operator/CLAUDE_CODE_OAUTH_TOKEN"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// TestComposeFullVaultPath_BareSuffix: a suffix without a leading
// slash must be stitched with one — the helper guarantees a
// well-formed path regardless of caller input.
func TestComposeFullVaultPath_BareSuffix(t *testing.T) {
	var u pgtype.UUID
	if err := u.Scan("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	got := composeFullVaultPath(u, "operator/X")
	want := "/aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee/operator/X"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}

// TestClassifyVaultError pins the M5.1 mapping from vault sentinels to
// chat.ErrorKind. The mapping is part of the operator UX (which
// remediation the dashboard surfaces) — silent drift would show up as
// a misleading error_kind in chat_messages.
func TestClassifyVaultError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want ErrorKind
	}{
		{"NotFound", vault.ErrVaultSecretNotFound, ErrorTokenNotFound},
		{"AuthExpired", vault.ErrVaultAuthExpired, ErrorTokenExpired},
		{"PermissionDenied → TokenExpired", vault.ErrVaultPermissionDenied, ErrorTokenExpired},
		{"WrappedAuthExpired", errors.New("wrap: " + vault.ErrVaultAuthExpired.Error()), ErrorVaultUnavailable},
		{"UnknownFallback", errors.New("transient i/o"), ErrorVaultUnavailable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyVaultError(c.in); got != c.want {
				t.Errorf("classifyVaultError(%v) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
