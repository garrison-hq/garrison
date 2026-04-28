// Package dockerexec is the supervisor's seam for invoking the local
// Docker daemon (currently via the garrison-docker-proxy TCP shim
// configured in docker-compose.yml). Two call shapes share the package:
//
//   - Run    — one-shot exec: write stdin, capture full stdout/stderr
//     buffers, return on subprocess exit. Used by mempalace
//     for `docker exec garrison-mempalace python -m mempalace.mcp`
//     one-shot invocations (bootstrap, wake-up, MCP server
//     spawning of the per-invocation mempalace MCP).
//
//   - RunStream — streaming exec: caller-supplied callbacks write to
//     the subprocess stdin and scan its stdout. Used by the
//     M5.1 chat runtime to pipe a multi-turn conversation
//     transcript via `docker run -i --rm garrison-claude:m5
//     -p --input-format stream-json` and stream NDJSON
//     stdout back through the spawn pipeline parser.
//
// The interface lets unit tests substitute a fake that records argv +
// returns canned output without exec'ing real Docker.
//
// Promoted from internal/mempalace/dockerexec.go in M5.1 — both
// mempalace and chat now depend on this seam, and a shared package
// keeps the call shapes evolving in one place. Behavior of the existing
// `Run` method is byte-for-byte identical to the M2.2 implementation.
package dockerexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"syscall"
)

// DockerExec is the call seam. Production uses RealDockerExec; tests
// inject fakes that capture argv and return canned output / errors.
type DockerExec interface {
	// Run executes a one-shot subprocess: writes stdin to completion,
	// captures full stdout/stderr buffers, returns when the subprocess
	// exits. Suitable for short-lived calls (bootstrap, wake-up).
	Run(ctx context.Context, args []string, stdin io.Reader) (stdout, stderr []byte, err error)

	// RunStream executes a streaming subprocess. The caller supplies
	// two callbacks:
	//   - writeStdin runs once after cmd.Start; the callback is
	//     responsible for writing the full input AND closing the
	//     WriteCloser when done so the subprocess sees EOF on stdin.
	//   - scanStdout runs in the foreground after writeStdin returns;
	//     the callback MUST drain the Reader to completion (concurrency
	//     rule 8: pipes drained before cmd.Wait).
	//
	// RunStream returns the constructed *exec.Cmd after both callbacks
	// have returned successfully so the caller can call cmd.Wait()
	// (and inspect ProcessState / exit code) on its own schedule. The
	// returned Cmd has Setpgid: true and Cancel = pgroup-SIGTERM
	// (concurrency rules 4, 7) — context-cancellation kills the whole
	// process group, not just the docker CLI's PID.
	//
	// On callback error or subprocess spawn failure, RunStream attempts
	// to kill the process group before returning so the caller doesn't
	// have to.
	RunStream(
		ctx context.Context,
		args []string,
		writeStdin func(stdin io.WriteCloser) error,
		scanStdout func(stdout io.Reader) error,
	) (*exec.Cmd, error)
}

// RealDockerExec is the production implementation. The DockerBin field
// pins the binary path (set by config.DockerBin); empty falls back to
// resolving "docker" via $PATH.
type RealDockerExec struct {
	DockerBin string
}

func (r RealDockerExec) bin() string {
	if r.DockerBin == "" {
		return "docker"
	}
	return r.DockerBin
}

// Run preserves the M2.2 one-shot exec semantics byte-for-byte.
func (r RealDockerExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// RunStream implements the streaming-exec contract documented on the
// interface. The two callbacks run sequentially: writeStdin first, then
// scanStdout, so the typical flow ("write the full transcript, close
// stdin to signal EOF, drain stdout NDJSON") works without the caller
// managing two goroutines. If a future caller needs interleaved
// stdin/stdout (interactive REPL shape), it can wrap this method or
// add a sibling method — M5.1's chat runtime is one-shot-stdin shaped.
func (r RealDockerExec) RunStream(
	ctx context.Context,
	args []string,
	writeStdin func(stdin io.WriteCloser) error,
	scanStdout func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, r.bin(), args...)

	// Concurrency rules 4 + 7: the docker CLI is the process leader of
	// its own group; the daemon-side container exits cleanly on `--rm`
	// when the CLI is signalled; signalling the group covers the rare
	// case where docker spawned helpers we'd otherwise orphan.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("dockerexec: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		return nil, fmt.Errorf("dockerexec: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		return nil, fmt.Errorf("dockerexec: start: %w", err)
	}

	// writeStdin owns closing the WriteCloser when its writes are done.
	// If the callback forgets to close, the subprocess will hang on
	// stdin EOF; that's a caller bug we surface by failing the test
	// rather than papering over with a defer here. We DO close on
	// callback error so we don't leak the pipe.
	if err := writeStdin(stdinPipe); err != nil {
		_ = stdinPipe.Close()
		// Force the subprocess to exit before we return so the caller
		// doesn't have to clean up.
		_ = killProcessGroup(cmd, syscall.SIGTERM)
		_ = cmd.Wait()
		return nil, fmt.Errorf("dockerexec: writeStdin: %w", err)
	}

	// scanStdout MUST drain to EOF (concurrency rule 8). The caller's
	// reader-loop terminates naturally when the subprocess closes its
	// stdout (after producing all output and exiting).
	if err := scanStdout(stdoutPipe); err != nil {
		_ = killProcessGroup(cmd, syscall.SIGTERM)
		_ = cmd.Wait()
		return nil, fmt.Errorf("dockerexec: scanStdout: %w", err)
	}

	return cmd, nil
}

// killProcessGroup is a local copy of internal/spawn/pgroup.go's
// helper. M5.1 keeps two copies (one in spawn, one here) per the T002
// scope discipline: the spawn-package version stays sealed M2.1 surface;
// this version owns the dockerexec-package process-group lifecycle.
// Both implement the same logic and share the same ESRCH-is-benign
// rationale documented at internal/spawn/pgroup.go.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("dockerexec: killProcessGroup called before cmd.Start()")
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			slog.Debug("process group already exited",
				"pid", pid, "signal", sig.String(), "err", err)
			return nil
		}
		return fmt.Errorf("dockerexec: signal %s to pgroup %d: %w", sig, pid, err)
	}
	return nil
}
