package mempalace

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Status is the value written to agent_instances.wake_up_status. Permitted
// values per FR-209: 'ok', 'failed', 'skipped'. M2.2 never writes Skipped.
type Status string

const (
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped" // reserved; M2.2 never writes this
)

// WakeupConfig wires Wakeup's collaborators and tunables. Timeout is a
// hard ceiling: any invocation still running when Timeout elapses is
// cancelled and the result reported as StatusFailed. Default 2s per
// NFR-202.
//
// Per T001 finding F2, `mempalace wake-up` in 3.3.2 accepts ONLY --wing;
// --palace is a top-level flag (before the `wake-up` subcommand); there
// is NO --max-tokens flag. WakeupConfig therefore carries no MaxTokens
// field. The Wakeup function inserts --palace at the correct position
// automatically.
type WakeupConfig struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	Timeout            time.Duration // default 2s (NFR-202)
	Logger             *slog.Logger
	Exec               DockerExec // injection seam; nil → RealDockerExec
}

// Wakeup runs `docker exec <container> mempalace --palace <path> wake-up
// --wing <wing>` with a Timeout-bounded context. Non-blocking on every
// failure mode: timeout, non-zero exit, docker not found, container not
// running — all map to (stdout="", StatusFailed, elapsed, nil). A true
// error is returned only if the caller's inputs are obviously invalid
// (empty container / path / wing); otherwise the spawn path can proceed
// with an empty wake-up block.
//
// The returned stdout is the MemPalace wake-up text verbatim (preamble +
// L0/L1 sections). Callers inject it into --system-prompt via
// ComposeSystemPrompt; if stdout is empty (either empty wing or failure),
// ComposeSystemPrompt omits the wake-up block.
func Wakeup(ctx context.Context, cfg WakeupConfig, wing string) (stdout string, status Status, elapsed time.Duration, err error) {
	if cfg.MempalaceContainer == "" {
		return "", StatusFailed, 0, errors.New("mempalace.Wakeup: MempalaceContainer is empty")
	}
	if cfg.PalacePath == "" {
		return "", StatusFailed, 0, errors.New("mempalace.Wakeup: PalacePath is empty")
	}
	if wing == "" {
		return "", StatusFailed, 0, errors.New("mempalace.Wakeup: wing is empty")
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	exe := cfg.Exec
	if exe == nil {
		exe = RealDockerExec{DockerBin: cfg.DockerBin}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"exec", cfg.MempalaceContainer,
		"mempalace", "--palace", cfg.PalacePath,
		"wake-up", "--wing", wing,
	}
	start := time.Now()
	out, errOut, execErr := exe.Run(runCtx, args, nil)
	elapsed = time.Since(start)

	if execErr != nil {
		logger.Warn("wake_up_failed",
			"palace_wing", wing,
			"elapsed_ms", elapsed.Milliseconds(),
			"error", execErr.Error(),
			"stderr", snippet(errOut),
		)
		return "", StatusFailed, elapsed, nil
	}
	return string(out), StatusOK, elapsed, nil
}

// ComposeSystemPrompt builds the Claude `--system-prompt` argument from
// three pieces: the agent's agent.md content (from agents.agent_md), the
// wake-up stdout (possibly empty), and the "This turn" block carrying
// ticketID + instanceID (per FR-207a as extended by Session 2026-04-23 Q2).
//
// When wakeUpStdout is empty (either a legitimately empty wing or a
// StatusFailed wake-up per FR-207b), the wake-up block is omitted entirely
// and the "---\n## Wake-up context\n" section does not appear. The "This
// turn" block always appears.
//
// The exact template shape matches FR-207a verbatim so a test can assert
// byte-equality against it.
func ComposeSystemPrompt(agentMD, wakeUpStdout, ticketID, instanceID string) string {
	thisTurn := "## This turn\n\nYou have been spawned as agent_instance " + instanceID +
		" to work ticket " + ticketID +
		". Read it, then execute your completion protocol.\n"

	if wakeUpStdout == "" {
		return agentMD + "\n\n---\n\n" + thisTurn
	}
	return agentMD + "\n\n---\n\n## Wake-up context\n\n" + wakeUpStdout +
		"\n\n---\n\n" + thisTurn
}
