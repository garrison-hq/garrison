//go:build linux

package spawn

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKillProcessGroupTerminatesChildren is the load-bearing assertion for
// M2.1's switch to process-group signalling: a subprocess that forks a
// child must be stopped along with the child when the supervisor signals
// the group, not left as an orphan the way PID-only signalling would.
//
// The fixture is an `sh` process that backgrounds a `sleep 30` and writes
// the sleep's PID to a file before `wait`-ing for it. We call
// killProcessGroup(cmd, SIGTERM), then verify within a second that both
// processes are gone. The test is Linux-only because the helper (and the
// Setpgid mechanism) is Linux-specific.
func TestKillProcessGroupTerminatesChildren(t *testing.T) {
	childPIDFile := filepath.Join(t.TempDir(), "child.pid")

	script := `sleep 30 & echo $! > "$PIDFILE"; wait`
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "PIDFILE="+childPIDFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Wait for the child PID to be written to disk. 500ms is more than
	// generous for `echo $! > file` on any Linux box.
	childPID := waitForChildPID(t, childPIDFile, 500*time.Millisecond)

	// Sanity: both processes exist before the signal.
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("parent not alive pre-signal: %v", err)
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("child not alive pre-signal: %v", err)
	}

	if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
		t.Fatalf("killProcessGroup: %v", err)
	}

	// Reap the parent so it doesn't linger as a zombie.
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cmd.Wait did not return within 2s of SIGTERM — process group still alive")
	}

	// The child (backgrounded `sleep 30`) must also be gone. SIGTERM hit
	// the whole group, and sh doesn't trap, so both received it.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("child pid %d still alive 1s after SIGTERM to group; stderr=%q",
		childPID, stderr.String())
}

// TestKillProcessGroupTolerantOfMissingGroup asserts the ESRCH-swallow
// contract: sending to an already-reaped process group is a benign race
// on every success path (Claude finishes cleanly before the supervisor
// notices), so the helper must not bubble the error up.
func TestKillProcessGroupTolerantOfMissingGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait: %v", err)
	}

	// The process is now fully reaped; its PID is either reused or simply
	// absent. syscall.Kill(-pid) will return ESRCH either way.
	if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
		t.Errorf("killProcessGroup on exited cmd returned error %v; want nil (ESRCH is benign)", err)
	}
}

func TestKillProcessGroupRejectsUnstartedCmd(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	err := killProcessGroup(cmd, syscall.SIGTERM)
	if err == nil {
		t.Fatal("killProcessGroup(unstarted): want error, got nil")
	}
}

// waitForChildPID polls for the test fixture's PID-file and returns the
// pid the parent shell wrote before it went into `wait`. Fails the test
// cleanly if the file never appears within the budget.
func waitForChildPID(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child PID file %s not written within %s", path, timeout)
	return 0
}
