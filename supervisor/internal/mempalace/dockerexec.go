// Package mempalace owns supervisor-side MemPalace integration: palace
// bootstrap, wake-up context capture, and construction of the per-invocation
// MCP server spec that Claude subprocesses use to reach MemPalace.
//
// All MemPalace calls from the supervisor flow through `docker exec` against
// the garrison-mempalace sidecar container, per the Session 2026-04-23
// Deployment clarification. The DockerExec seam in this file lets unit tests
// substitute a fake executor without spawning real docker.
//
// Spike findings F1, F2, F5 govern the call shapes produced by Bootstrap,
// Wakeup, and MCPServerSpec respectively. See `specs/004-m2-2-mempalace/
// research.md` for the verbatim findings and `plan.md` §"Deployment
// topology" for the overall shape.
package mempalace

import (
	"bytes"
	"context"
	"io"
	"os/exec"
)

// DockerExec is the `docker exec` seam. Production uses RealDockerExec.
// Unit tests inject a fake that captures the argv it was called with and
// returns canned stdout / stderr / error.
//
// The interface is intentionally minimal: args is the argv handed to
// `docker` (so callers construct the full "exec -i <container> ..." tail);
// stdin is an optional reader whose bytes feed the subprocess's stdin.
// stdout and stderr are captured in full; streaming is unnecessary for the
// call patterns M2.2 uses (bootstrap is one-shot, wake-up is one-shot,
// MCP server spawning is Claude's concern not ours).
type DockerExec interface {
	Run(ctx context.Context, args []string, stdin io.Reader) (stdout, stderr []byte, err error)
}

// RealDockerExec implements DockerExec against os/exec. The DockerBin field
// lets callers pin the path (set by config.DockerBin on the production
// path); an empty DockerBin falls back to "docker" via $PATH.
type RealDockerExec struct {
	DockerBin string
}

// Run executes `DockerBin <args>` with a context-derived lifetime. The
// returned stdout/stderr buffers reflect the full captured output. On exit
// code != 0 the error is an *exec.ExitError (or wrapped underlying error
// for I/O / spawn failures); callers distinguish "ran, non-zero" from
// "couldn't run at all" by type assertion where needed.
func (r RealDockerExec) Run(ctx context.Context, args []string, stdin io.Reader) ([]byte, []byte, error) {
	bin := r.DockerBin
	if bin == "" {
		bin = "docker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}
