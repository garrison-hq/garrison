package dockerexec

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The tests use /bin/sh -c "<inline script>" as a stand-in for the
// docker CLI so RunStream's pipe/process-group/wait semantics can be
// exercised without a Docker daemon. The RealDockerExec.DockerBin
// field is the seam: setting it to /bin/sh makes args the shell's argv,
// not docker's.

func newShExec() RealDockerExec {
	return RealDockerExec{DockerBin: "/bin/sh"}
}

// TestRealDockerExec_RunStream_ClosesStdinAfterCallback asserts that a
// writeStdin callback which writes some bytes and closes the
// WriteCloser produces a subprocess seeing EOF on stdin — verifiable
// because `cat` exits cleanly only when its stdin closes. If RunStream
// (or the caller's callback) failed to close stdin, `cat` would hang
// forever and the surrounding context-deadline would fire.
func TestRealDockerExec_RunStream_ClosesStdinAfterCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const payload = "hello-from-test\n"

	var captured bytes.Buffer
	cmd, err := newShExec().RunStream(
		ctx,
		[]string{"-c", "cat"},
		func(stdin io.WriteCloser) error {
			defer stdin.Close()
			if _, err := io.WriteString(stdin, payload); err != nil {
				return err
			}
			return nil
		},
		func(stdout io.Reader) error {
			_, err := io.Copy(&captured, stdout)
			return err
		},
	)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait: %v", err)
	}

	if got := captured.String(); got != payload {
		t.Fatalf("expected stdout to echo stdin %q, got %q", payload, got)
	}
}

// TestRealDockerExec_RunStream_DrainsStdoutBeforeWait asserts that
// RunStream returns only after scanStdout has drained the pipe to EOF.
// AGENTS.md concurrency rule 8: pipes drained before cmd.Wait. We
// verify by emitting many lines, asserting all of them land in the
// scanStdout callback, and asserting cmd.Wait succeeds (no truncated-
// read errors per the rule-8 documentation).
func TestRealDockerExec_RunStream_DrainsStdoutBeforeWait(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const lines = 64

	var lineCount int64
	cmd, err := newShExec().RunStream(
		ctx,
		[]string{"-c", `for i in $(seq 1 64); do echo "line-$i"; done`},
		func(stdin io.WriteCloser) error {
			return stdin.Close()
		},
		func(stdout io.Reader) error {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if !strings.HasPrefix(scanner.Text(), "line-") {
					return errors.New("unexpected line shape")
				}
				atomic.AddInt64(&lineCount, 1)
			}
			return scanner.Err()
		},
	)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait: %v (drain incomplete?)", err)
	}

	if got := atomic.LoadInt64(&lineCount); got != lines {
		t.Fatalf("expected %d lines drained from stdout, got %d", lines, got)
	}
}

// TestRealDockerExec_RunStream_PropagatesWriteStdinError covers the
// branch where the writeStdin callback returns an error: RunStream
// must signal-kill the subprocess + Wait + return the wrapped error
// rather than leaking the process.
func TestRealDockerExec_RunStream_PropagatesWriteStdinError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wantErr := errors.New("synthetic write failure")

	_, err := newShExec().RunStream(
		ctx,
		[]string{"-c", "cat"},
		func(stdin io.WriteCloser) error {
			return wantErr
		},
		func(stdout io.Reader) error {
			t.Fatalf("scanStdout should not be called when writeStdin errors")
			return nil
		},
	)
	if err == nil {
		t.Fatalf("expected error from writeStdin propagation, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped %v, got %v", wantErr, err)
	}
}

// TestRealDockerExec_Run_RoundTripsStdinAndStdout exercises the
// existing one-shot Run method (M2.2 byte-for-byte preserved) so the
// move from internal/mempalace/dockerexec.go is verified at the
// behavioural level by this package's own tests, not just by the
// transitive mempalace suite.
func TestRealDockerExec_Run_RoundTripsStdinAndStdout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stdout, stderr, err := newShExec().Run(
		ctx,
		[]string{"-c", "cat"},
		strings.NewReader("round-trip-payload\n"),
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(stdout); got != "round-trip-payload\n" {
		t.Fatalf("stdout: got %q", got)
	}
	if len(stderr) != 0 {
		t.Fatalf("stderr: expected empty, got %q", stderr)
	}
}
