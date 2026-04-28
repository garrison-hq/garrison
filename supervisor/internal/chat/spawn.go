package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpconfig"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// SpawnTurn is the per-message docker-run orchestrator. Called by the
// chat.Worker after the operator message lands and pre-checks (session
// active, cost cap not reached, assistant 'pending' row inserted) have
// passed.
//
// Steps:
//  1. Fetch CLAUDE_CODE_OAUTH_TOKEN from vault (single-secret reveal
//     using a synthesized GrantRow). Audit row written by the M2.3
//     Fetch path. On failure → terminal-write the assistant row with
//     the categorised error_kind and return.
//  2. Build per-message MCP config + write to disk; bind-mount into
//     the chat container at /etc/garrison/mcp.json:ro.
//  3. Construct the docker run argv per plan §"Container docker run argv"
//     (--input-format stream-json --tools "" --strict-mcp-config etc.).
//  4. Call dockerexec.RunStream: writeStdin pipes the assembled
//     transcript, scanStdout runs spawn.Run with a fresh ChatPolicy.
//  5. cmd.Wait(); cleanup MCP config file; SecretValue.Zero(); return.
func (deps Deps) SpawnTurn(
	ctx context.Context,
	sessionID, messageID pgtype.UUID,
	transcript []byte,
	mcpExtraEnv MempalaceWiring, // bootstrap-set DOCKER_HOST + container name
	supervisorBin string,
	agentRoDSN string,
) error {
	// Step 1 — vault reveal.
	grant := vault.GrantRow{
		EnvVarName: "CLAUDE_CODE_OAUTH_TOKEN",
		SecretPath: composeFullVaultPath(deps.CustomerID, deps.OAuthVaultPath),
		CustomerID: deps.CustomerID,
	}
	fetched, err := deps.VaultClient.Fetch(ctx, []vault.GrantRow{grant})
	if err != nil {
		ek := classifyVaultError(err)
		deps.Logger.Error("chat: vault fetch failed",
			"session_id", uuidString(sessionID),
			"message_id", uuidString(messageID),
			"err", err, "error_kind", ek)
		writeAssistantError(ctx, deps, messageID, ek)
		return err
	}
	tokenSV, ok := fetched["CLAUDE_CODE_OAUTH_TOKEN"]
	if !ok || tokenSV.Empty() {
		writeAssistantError(ctx, deps, messageID, ErrorTokenNotFound)
		return errors.New("chat: vault returned no token")
	}
	defer tokenSV.Zero()

	// Step 2 — MCP config write.
	mcpPath, err := writeChatMCPConfig(deps, messageID, supervisorBin, agentRoDSN, mcpExtraEnv)
	if err != nil {
		deps.Logger.Error("chat: mcp config write failed", "err", err)
		writeAssistantError(ctx, deps, messageID, ErrorClaudeRuntimeError)
		return err
	}
	defer func() { _ = os.Remove(mcpPath) }()

	// Step 3 — docker run argv.
	args := dockerRunArgs(deps, mcpPath, tokenEnvSpec(tokenSV))

	// Step 4 — RunStream wires writeStdin (transcript + close) +
	// scanStdout (spawn.Run with ChatPolicy).
	policy := NewChatPolicy(deps, sessionID, messageID)
	cmd, err := deps.DockerExec.RunStream(
		ctx,
		args,
		func(stdin io.WriteCloser) error {
			defer stdin.Close()
			if _, werr := stdin.Write(transcript); werr != nil {
				return fmt.Errorf("write transcript: %w", werr)
			}
			return nil
		},
		func(stdout io.Reader) error {
			_, runErr := spawn.Run(ctx, stdout, policy, deps.Logger)
			return runErr
		},
	)
	if err != nil {
		deps.Logger.Error("chat: docker RunStream failed", "err", err)
		writeAssistantError(ctx, deps, messageID, ErrorDockerProxyUnreachable)
		return err
	}

	// Step 5 — cmd.Wait
	if err := cmd.Wait(); err != nil {
		// If the assistant row was already terminal-written by
		// ChatPolicy.OnResult, this is a benign exit code. Otherwise
		// the container died unexpectedly; mark container_crashed.
		// We can't atomically check the row state without a tx, so
		// best-effort: log + write only if the policy didn't.
		if policy.resultEvent == nil {
			writeAssistantError(ctx, deps, messageID, ErrorContainerCrashed)
		}
		deps.Logger.Warn("chat: docker subprocess exited non-zero",
			"err", err, "session_id", uuidString(sessionID),
			"message_id", uuidString(messageID))
	}
	return nil
}

// MempalaceWiring carries the runtime values the chat MCP config
// needs from the supervisor's mempalace bootstrap. Constructed in
// cmd/supervisor/main.go alongside Deps.
type MempalaceWiring struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string
}

func writeChatMCPConfig(deps Deps, messageID pgtype.UUID, supervisorBin, agentRoDSN string, wiring MempalaceWiring) (string, error) {
	idText := uuidString(messageID)
	if idText == "" {
		return "", errors.New("chat: writeChatMCPConfig: invalid messageID")
	}
	body, err := mcpconfig.BuildChatConfig(mcpconfig.ChatConfigParams{
		SupervisorBin: supervisorBin,
		AgentRoDSN:    agentRoDSN,
		Mempalace: mcpconfig.MempalaceParams{
			DockerBin:          wiring.DockerBin,
			MempalaceContainer: wiring.MempalaceContainer,
			PalacePath:         wiring.PalacePath,
			DockerHost:         wiring.DockerHost,
		},
	})
	if err != nil {
		return "", err
	}
	if deps.MCPConfigDir == "" {
		return "", errors.New("chat: writeChatMCPConfig: MCPConfigDir unset")
	}
	if err := os.MkdirAll(deps.MCPConfigDir, 0o750); err != nil {
		return "", fmt.Errorf("mkdir mcp dir: %w", err)
	}
	path := filepath.Join(deps.MCPConfigDir, "chat-"+idText+".json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("write mcp file: %w", err)
	}
	return path, nil
}

func dockerRunArgs(deps Deps, mcpHostPath string, tokenEnv string) []string {
	model := deps.DefaultModel
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	network := deps.DockerNetwork
	if network == "" {
		network = "garrison-net"
	}
	return []string{
		"run", "--rm", "-i",
		"-e", tokenEnv,
		"-v", mcpHostPath + ":/etc/garrison/mcp.json:ro",
		"--network", network,
		deps.ChatContainerImage,
		"-p",
		"--verbose",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--tools", "",
		"--mcp-config", "/etc/garrison/mcp.json",
		"--strict-mcp-config",
		"--permission-mode", "bypassPermissions",
		"--model", model,
	}
}

// tokenEnvSpec returns the "VAR=value" env-pair string for the
// docker -e flag. UnsafeBytes is the only safe place to materialise
// the secret value (vaultlog analyzer-allowed; matches
// internal/spawn/spawn.go's pattern).
//
//nolint:vaultlog // env-var injection is the documented exception path
func tokenEnvSpec(sv vault.SecretValue) string {
	return "CLAUDE_CODE_OAUTH_TOKEN=" + string(sv.UnsafeBytes())
}

// composeFullVaultPath joins customer_id with the path suffix per the
// M2.3 splitSecretPath convention (plan §5.3).
func composeFullVaultPath(customerID pgtype.UUID, suffix string) string {
	if !customerID.Valid {
		return suffix
	}
	cid := uuidString(customerID)
	if suffix == "" {
		return "/" + cid
	}
	if len(suffix) > 0 && suffix[0] != '/' {
		return "/" + cid + "/" + suffix
	}
	return "/" + cid + suffix
}

// classifyVaultError maps vault.* sentinels to chat.ErrorKind.
func classifyVaultError(err error) ErrorKind {
	switch {
	case errors.Is(err, vault.ErrVaultSecretNotFound):
		return ErrorTokenNotFound
	case errors.Is(err, vault.ErrVaultAuthExpired):
		return ErrorTokenExpired
	default:
		return ErrorVaultUnavailable
	}
}

// writeAssistantError is the WithoutCancel + grace error-path terminal
// writer used when SpawnTurn fails before ChatPolicy can take over.
func writeAssistantError(ctx context.Context, deps Deps, messageID pgtype.UUID, ek ErrorKind) {
	wctx := context.WithoutCancel(ctx)
	if deps.TerminalWriteGrace > 0 {
		var cancel context.CancelFunc
		wctx, cancel = context.WithTimeout(wctx, deps.TerminalWriteGrace)
		defer cancel()
	}
	ekVal := ek
	if err := deps.Queries.TerminalWriteWithError(wctx, store.TerminalWriteWithErrorParams{
		ID:        messageID,
		Status:    "failed",
		ErrorKind: &ekVal,
	}); err != nil {
		deps.Logger.Error("chat: writeAssistantError failed",
			"err", err, "error_kind", ek, "message_id", uuidString(messageID))
	}
}
