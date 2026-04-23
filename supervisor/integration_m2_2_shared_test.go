//go:build integration || chaos

// Shared helpers for the M2.2 integration and chaos test files.
// Split from the integration-tagged files so the chaos suite (which
// has its own build tag) can reuse requireSpikeStack, waitFor, tail,
// and the m22 spike-stack constants.

package supervisor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Spike-stack constants. Reused from T001. Container names + proxy IP
// on the docker bridge.
const (
	m22MempalaceContainer = "spike-mempalace"
	m22DockerProxyHost    = "tcp://172.18.0.3:2375"
)

// requireSpikeStack checks that the spike-mempalace + spike-docker-proxy
// containers from T001 are running. Skips the test if they're not.
func requireSpikeStack(t *testing.T) {
	t.Helper()
	for _, name := range []string{m22MempalaceContainer, "spike-docker-proxy"} {
		out, err := exec.Command("docker", "ps", "--filter", "name="+name, "--format", "{{.Names}}").CombinedOutput()
		if err != nil || len(out) == 0 {
			t.Skipf("spike stack not running (container %q not found); skipping M2.2 test. "+
				"Run the T001 validation spike at ~/scratch/m2-2-topology-spike/ first, or "+
				"stand up supervisor/docker-compose.yml equivalent.", name)
		}
	}
}

func repoFile(t *testing.T, relPath string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(cwd, relPath)
}

func waitFor(ctx context.Context, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok, err := check()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("waitFor timed out after %s", timeout)
}

func ptrVal(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func numericToFloat(n pgtype.Numeric) (float64, error) {
	b, err := json.Marshal(n)
	if err != nil {
		return 0, err
	}
	var num json.Number
	if err := json.Unmarshal(b, &num); err != nil {
		return 0, err
	}
	f, err := num.Float64()
	if err != nil {
		return 0, err
	}
	return f, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "... (truncated) ..." + s[len(s)-n:]
}

func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	const hex = "0123456789abcdef"
	out := make([]byte, 36)
	j := 0
	for i, x := range b {
		out[j] = hex[x>>4]
		j++
		out[j] = hex[x&0x0f]
		j++
		if i == 3 || i == 5 || i == 7 || i == 9 {
			out[j] = '-'
			j++
		}
	}
	return string(out)
}
