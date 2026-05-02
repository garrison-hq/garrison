package spawn

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/garrison-hq/garrison/supervisor/internal/agentpolicy"
	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// M7 spawn-side wiring lives in this file so the legacy spawn.go
// pre-M7 surface stays self-contained and the diff for the soak window
// is easy to walk.

// recordM7HashesForInstance updates the just-INSERTed agent_instances
// row with the M7 forensic hashes. Call site is spawn.runRealClaude
// step 3a, immediately after the agent is resolved + before the
// (possibly slow) wake-up RPC. Failures here are best-effort: the spawn
// continues even if the UPDATE fails — the row's image_digest /
// preamble_hash columns default to empty strings so a missing UPDATE
// leaves them empty rather than corrupt.
func recordM7HashesForInstance(
	ctx context.Context,
	deps Deps,
	instanceID pgtype.UUID,
	dept store.Department,
	agent agents.Agent,
) error {
	imageDigest := ""
	if agent.PalaceWing != nil {
		// Until T014's grandfathering migration runs, no agent has an
		// image_digest column populated. Once migrate7 runs, agents.id
		// → image_digest mapping is populated; until then, leave the
		// instance row's image_digest at its '' default.
	}
	claudeMD := computeClaudeMDHash(dept)
	return deps.Queries.UpdateInstanceM7Hashes(ctx, store.UpdateInstanceM7HashesParams{
		ID:           instanceID,
		PreambleHash: agentpolicy.Hash(),
		ClaudeMdHash: claudeMD,
		ImageDigest:  imageDigest,
	})
}

// computeClaudeMDHash returns the SHA-256 of the workspace's CLAUDE.md
// file when one exists at the expected path, NULL otherwise. Claude
// Code's auto-discovery looks at the cwd; we mirror that here so the
// recorded hash matches what Claude actually loaded at spawn time.
func computeClaudeMDHash(dept store.Department) *string {
	if dept.WorkspacePath == nil || *dept.WorkspacePath == "" {
		return nil
	}
	path := filepath.Join(*dept.WorkspacePath, "CLAUDE.md")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil
	}
	hex := hex.EncodeToString(h.Sum(nil))
	return &hex
}

// runViaContainerInputs is the parameter bundle for
// runRealClaudeViaContainer. Kept as a struct so the call site stays
// readable and future fields (e.g. claude env vars threaded through
// AgentContainer.Exec) don't blow up the function signature.
type runViaContainerInputs struct {
	InstanceID pgtype.UUID
	EventID    pgtype.UUID
	TicketID   pgtype.UUID
	RoleSlug   string
	Argv       []string
	Logger     *slog.Logger
}

// containerNameForRole builds the per-agent container name. T014's
// migrate7 uses the same format so the supervisor can address the
// already-running container via its name — no agentid→containerID
// lookup table required at the call site.
//
// Naming convention: garrison-agent-<role-slug>. Sandbox Rule 1 names
// containers per-agent; the slug is unique within a department per the
// agents schema.
func containerNameForRole(roleSlug string) string {
	return "garrison-agent-" + roleSlug
}

// ErrContainerControllerMissing surfaces when UseDirectExec=false but
// AgentContainer is nil. Spawn writes ExitSpawnFailed and the operator
// gets a clear log line pointing at the misconfiguration.
var ErrContainerControllerMissing = errors.New("spawn: AgentContainer Controller is nil; cannot exec via container path")

// runRealClaudeViaContainer is the M7 docker-exec spawn path. Called
// when cfg.UseDirectExec is false and AgentContainer is wired. Mirrors
// the legacy direct-exec branch's claude argv but routes the call
// through the per-agent container's already-running claude process.
//
// The full pipe-drain + adjudicator integration is captured here in
// outline form. The legacy path's piping pattern (claudeproto's
// NDJSON scanner over StdoutPipe) ports cleanly to the io.ReadClosers
// the Controller.Exec returns; T017 / T018 integration tests exercise
// the end-to-end flow.
func runRealClaudeViaContainer(
	ctx context.Context,
	deps Deps,
	inv runViaContainerInputs,
) error {
	logger := inv.Logger.With("via", "agent_container")
	if deps.AgentContainer == nil {
		logger.Error("agent container controller not wired", "err", ErrContainerControllerMissing)
		return writeFailContainerPath(ctx, deps, inv, ExitSpawnFailed)
	}
	containerID := containerNameForRole(inv.RoleSlug)
	stdout, stderr, err := deps.AgentContainer.Exec(ctx, containerID, append([]string{"claude"}, inv.Argv...), nil)
	if err != nil {
		logger.Error("AgentContainer.Exec failed", "container_id", containerID, "err", err)
		return writeFailContainerPath(ctx, deps, inv, ExitSpawnFailed)
	}
	defer func() {
		if stdout != nil {
			_ = stdout.Close()
		}
		if stderr != nil {
			_ = stderr.Close()
		}
	}()
	// Drain stderr to discard until we wire the full pipeline pass; the
	// goroutine prevents a backed-up stderr buffer from blocking the
	// stdout reader.
	if stderr != nil {
		go func() {
			_, _ = io.Copy(io.Discard, stderr)
		}()
	}

	// Stream NDJSON via the same scanner the legacy path uses. T017
	// wires this through claudeproto.RunPipeline; T011 lands the
	// branch shape so the integration tests can feed real container
	// output through it.
	if stdout != nil {
		scan := bufio.NewScanner(stdout)
		scan.Buffer(make([]byte, 64*1024), 1<<20)
		var lines int
		for scan.Scan() {
			lines++
		}
		logger.Info("agent container exec drained",
			"container_id", containerID, "ndjson_lines", lines)
	}

	if deps.Pool == nil {
		return nil
	}
	return writeTerminalCostAndWakeup(ctx, deps, terminalWriteParams{
		InstanceID: inv.InstanceID,
		EventID:    inv.EventID,
		TicketID:   inv.TicketID,
		Status:     "completed",
		ExitReason: "",
	})
}

func writeFailContainerPath(ctx context.Context, deps Deps, inv runViaContainerInputs, exitReason string) error {
	if deps.Pool == nil {
		return nil
	}
	if err := writeTerminalCostAndWakeup(ctx, deps, terminalWriteParams{
		InstanceID: inv.InstanceID,
		EventID:    inv.EventID,
		TicketID:   inv.TicketID,
		Status:     "failed",
		ExitReason: exitReason,
	}); err != nil {
		return fmt.Errorf("agent_container: terminal write: %w", err)
	}
	return nil
}
