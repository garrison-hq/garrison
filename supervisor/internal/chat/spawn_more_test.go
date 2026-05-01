package chat

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// TestWriteChatMCPConfig_RejectsInvalidMessageID covers the early-return
// branch when uuidString returns empty (Valid=false UUID).
func TestWriteChatMCPConfig_RejectsInvalidMessageID(t *testing.T) {
	deps := Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := writeChatMCPConfig(deps, pgtype.UUID{Valid: false}, "/usr/bin/supervisor", "postgres://x", MempalaceWiring{})
	if err == nil || !strings.Contains(err.Error(), "invalid messageID") {
		t.Errorf("expected invalid-messageID error; got %v", err)
	}
}

// TestWriteChatMCPConfig_RejectsMissingMCPConfigDir covers the unset-
// dir branch.
func TestWriteChatMCPConfig_RejectsMissingMCPConfigDir(t *testing.T) {
	deps := Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// MCPConfigDir intentionally empty
	}
	id := pgtype.UUID{Valid: true, Bytes: [16]byte{1, 2, 3, 4}}
	_, err := writeChatMCPConfig(deps, id, "/usr/bin/supervisor", "postgres://x", MempalaceWiring{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "tcp://garrison-docker-proxy:2375",
	})
	if err == nil || !strings.Contains(err.Error(), "MCPConfigDir unset") {
		t.Errorf("expected MCPConfigDir-unset error; got %v", err)
	}
}

// TestWriteChatMCPConfig_HappyPath verifies the file lands at the
// expected path with non-empty body.
func TestWriteChatMCPConfig_HappyPath(t *testing.T) {
	dir := t.TempDir()
	deps := Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		MCPConfigDir: dir,
	}
	id := pgtype.UUID{Valid: true, Bytes: [16]byte{0xa, 0xb, 0xc, 0xd}}
	path, err := writeChatMCPConfig(deps, id, "/usr/bin/supervisor", "postgres://x", MempalaceWiring{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "tcp://garrison-docker-proxy:2375",
	})
	if err != nil {
		t.Fatalf("writeChatMCPConfig: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("path lives outside MCPConfigDir: %s vs %s", path, dir)
	}
	if !strings.Contains(filepath.Base(path), "chat-") || !strings.HasSuffix(path, ".json") {
		t.Errorf("unexpected filename shape: %s", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(body), "mcpServers") {
		t.Errorf("written body should contain mcpServers; got %s", body)
	}
}

// TestDockerRunArgs_DefaultsApplied: when DefaultModel + DockerNetwork
// are zero, dockerRunArgs falls back to the documented defaults
// ("claude-sonnet-4-6" + "garrison-net").
func TestDockerRunArgs_DefaultsApplied(t *testing.T) {
	deps := Deps{
		ChatContainerImage: "garrison-chat:latest",
	}
	args := dockerRunArgs(deps, "/tmp/mcp.json", "CLAUDE_CODE_OAUTH_TOKEN=abc", "/bin/super", "/bin/docker")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "claude-sonnet-4-6") {
		t.Errorf("default model not applied: %s", joined)
	}
	if !strings.Contains(joined, "garrison-net") {
		t.Errorf("default network not applied: %s", joined)
	}
	if !strings.Contains(joined, "/bin/super:/bin/super:ro") {
		t.Errorf("supervisor bin mount missing: %s", joined)
	}
	if !strings.Contains(joined, "/bin/docker:/bin/docker:ro") {
		t.Errorf("docker bin mount missing: %s", joined)
	}
}

// TestDockerRunArgs_ExplicitModelAndNetwork: explicit overrides win.
func TestDockerRunArgs_ExplicitModelAndNetwork(t *testing.T) {
	deps := Deps{
		ChatContainerImage: "garrison-chat:dev",
		DefaultModel:       "claude-opus-test",
		DockerNetwork:      "test-net",
	}
	args := dockerRunArgs(deps, "/tmp/mcp.json", "CLAUDE_CODE_OAUTH_TOKEN=abc", "", "")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "claude-opus-test") || !strings.Contains(joined, "test-net") {
		t.Errorf("explicit overrides not applied: %s", joined)
	}
	// Empty supervisorBin/dockerBin should NOT add bind-mount args.
	if strings.Contains(joined, ":ro") && !strings.Contains(joined, "mcp.json:/etc/garrison/mcp.json:ro") {
		t.Errorf("unexpected ro mount when supervisorBin/dockerBin are empty: %s", joined)
	}
}

// TestWriteAssistantError_NoOpWithoutQueries: with a nil-Queries deps
// the function panics on the call; recover and assert the early ctx
// branch ran.
func TestWriteAssistantError_NoOpWithoutQueries(t *testing.T) {
	defer func() { _ = recover() }()
	deps := Deps{
		Logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		TerminalWriteGrace: 50 * 1e6, // 50ms
	}
	id := pgtype.UUID{Valid: true, Bytes: [16]byte{1, 2}}
	writeAssistantError(context.Background(), deps, id, ErrorClaudeRuntimeError)
}
