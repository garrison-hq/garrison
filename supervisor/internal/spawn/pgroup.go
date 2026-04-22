//go:build linux

package spawn

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"syscall"
)

// killProcessGroup delivers sig to the whole process group whose leader is
// cmd.Process. The caller MUST have set cmd.SysProcAttr.Setpgid = true
// before cmd.Start() so the subprocess is also the leader of its own
// group; syscall.Kill with a negative PID then addresses that group.
// Plan.md §"Process-group termination".
//
// This is M2.1's answer to the M1 PID-only signalling that lost track of
// any children Claude spawned (MCP servers, ripgrep, etc). The real-Claude
// path in spawn.go calls this helper from three sites: the MCP-bail in
// pipeline.Run, the timeout escalation installed via cmd.Cancel /
// cmd.WaitDelay, and the supervisor-shutdown path.
//
// ESRCH (no such process / process group) is a benign race: the leader
// already exited before we got around to signalling it, which is the
// expected outcome on every successful run where Claude finishes cleanly.
// Those are logged at debug and do not surface as an error.
func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return errors.New("spawn: killProcessGroup called before cmd.Start()")
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			slog.Debug("process group already exited",
				"pid", pid, "signal", sig.String(), "err", err)
			return nil
		}
		return fmt.Errorf("spawn: signal %s to pgroup %d: %w", sig, pid, err)
	}
	return nil
}
