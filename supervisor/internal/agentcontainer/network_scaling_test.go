//go:build integration

package agentcontainer

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBridgeScalesToTwentyFiveAgents is the Q1 pre-implementation
// acceptance gate (decision #20). The original plan committed to
// N≥50 networks; the operator lowered the bar to N=25 once it was
// observed that Docker's default user-defined-bridge ceiling sits
// near 31, making N=50 a kernel-tuning question rather than a Garrison
// design question. N=25 sits comfortably below the bridge limit and
// matches realistic alpha-band single-operator-deploy agent counts.
//
// If this test fails on the M7 host, plan amends to overlay or
// shared-bridge networking before T006 starts; the M7 retro records
// the host-specific cause.
//
// The test is `-tags=integration` because it provisions real Docker
// networks + containers; it skips cleanly if Docker isn't reachable.
//
// Strategy:
//
//  1. Skip if `docker` binary is missing or the daemon is unreachable.
//  2. Create N=25 user-defined bridge networks named
//     m7-bridge-scaling-test-NN.
//  3. Run a `sleep 60` alpine container on each network.
//  4. Verify all 25 containers report State=running.
//  5. Tear down (docker rm -f containers, docker network rm networks),
//     even on test failure (t.Cleanup).
//
// The test deliberately uses the docker CLI rather than going through
// agentcontainer.Controller — Controller is verified by the unit
// tests in controller_test.go; this test exists to confirm Docker
// itself can scale to N agent-networks, which is a kernel/daemon
// question, not a Garrison-code question.
func TestBridgeScalesToTwentyFiveAgents(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not on PATH; skipping bridge-scaling test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Daemon reachability probe.
	if out, err := dockerCmd(ctx, "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Skipf("docker daemon unreachable (%v); skipping. Output: %s", err, out)
	}

	const (
		n             = 25
		netPrefix     = "m7-bridge-scaling-test"
		containerName = "m7-bridge-scaling-agent"
		image         = "alpine:3.20"
	)

	// Pre-flight: ensure alpine image is present (cheap if cached).
	if out, err := dockerCmd(ctx, "pull", image); err != nil {
		t.Fatalf("docker pull %s: %v\n%s", image, err, out)
	}

	// Cleanup helper — runs always, even on failure or panic.
	cleanup := func() {
		// Best-effort; ignore errors — the test body may have torn
		// some of these down already.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cleanupCancel()
		for i := 0; i < n; i++ {
			_, _ = dockerCmd(cleanupCtx, "rm", "-f", fmt.Sprintf("%s-%02d", containerName, i))
			_, _ = dockerCmd(cleanupCtx, "network", "rm", fmt.Sprintf("%s-%02d", netPrefix, i))
		}
	}
	t.Cleanup(cleanup)
	// Defensive: also clean before the test starts (in case a prior
	// run left state behind).
	cleanup()

	// Create N networks + N containers.
	for i := 0; i < n; i++ {
		net := fmt.Sprintf("%s-%02d", netPrefix, i)
		ctr := fmt.Sprintf("%s-%02d", containerName, i)

		if out, err := dockerCmd(ctx, "network", "create", "--driver", "bridge", net); err != nil {
			t.Fatalf("network create %s (%d/%d): %v\n%s", net, i+1, n, err, out)
		}
		if out, err := dockerCmd(ctx,
			"run", "-d", "--rm",
			"--name", ctr,
			"--network", net,
			"--label", "garrison.test=m7-bridge-scaling",
			image, "sleep", "120",
		); err != nil {
			t.Fatalf("run %s (%d/%d): %v\n%s", ctr, i+1, n, err, out)
		}
	}

	// Verify all 25 containers report Status=running.
	out, err := dockerCmd(ctx,
		"ps", "--filter", "label=garrison.test=m7-bridge-scaling",
		"--format", "{{.Names}}\t{{.Status}}",
	)
	if err != nil {
		t.Fatalf("docker ps: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != n {
		t.Fatalf("expected %d running containers; got %d\n%s", n, len(lines), out)
	}
	for _, line := range lines {
		if !strings.Contains(line, "Up ") {
			t.Errorf("container not Up: %s", line)
		}
	}

	// Verify each network has its container attached. We only
	// inspect a sample (first + middle + last) to keep the test
	// runtime tight; structural failure modes hit the whole batch.
	for _, i := range []int{0, n / 2, n - 1} {
		net := fmt.Sprintf("%s-%02d", netPrefix, i)
		ctr := fmt.Sprintf("%s-%02d", containerName, i)
		netOut, err := dockerCmd(ctx, "network", "inspect", net,
			"--format", "{{range .Containers}}{{.Name}}\n{{end}}")
		if err != nil {
			t.Fatalf("network inspect %s: %v\n%s", net, err, netOut)
		}
		if !strings.Contains(netOut, ctr) {
			t.Errorf("network %s does not contain expected container %s; got: %s",
				net, ctr, netOut)
		}
	}
}

// dockerCmd shells out to the docker CLI with the given args.
// Returns combined stdout+stderr and the exec error.
func dockerCmd(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
