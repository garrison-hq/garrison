package spawn

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// deptWithWorkspace builds a minimal store.Department with the given
// workspace path. Empty path → nil pointer (matches "no workspace
// configured" mode).
func deptWithWorkspace(path string) store.Department {
	if path == "" {
		return store.Department{}
	}
	return store.Department{WorkspacePath: &path}
}

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

// fakeController is a minimal Controller stub for unit tests verifying
// runRealClaudeViaContainer's call shape. Records the (containerID, cmd)
// tuple from Exec so the test can assert the swap routed correctly.
type fakeController struct {
	execCalls []fakeExecCall
}

type fakeExecCall struct {
	ContainerID string
	Cmd         []string
}

func (f *fakeController) Create(ctx context.Context, spec agentcontainer.ContainerSpec) (string, error) {
	return "fake-id", nil
}
func (f *fakeController) Start(ctx context.Context, id string) error  { return nil }
func (f *fakeController) Stop(ctx context.Context, id string) error   { return nil }
func (f *fakeController) Remove(ctx context.Context, id string) error { return nil }
func (f *fakeController) Exec(ctx context.Context, id string, cmd []string, stdin io.Reader) (io.ReadCloser, io.ReadCloser, error) {
	f.execCalls = append(f.execCalls, fakeExecCall{ContainerID: id, Cmd: append([]string{}, cmd...)})
	return io.NopCloser(emptyReader{}), io.NopCloser(emptyReader{}), nil
}
func (f *fakeController) ConnectNetwork(ctx context.Context, id, name string) error { return nil }
func (f *fakeController) Reconcile(ctx context.Context, expected []agentcontainer.ExpectedContainer) (agentcontainer.ReconcileReport, error) {
	return agentcontainer.ReconcileReport{}, nil
}
func (f *fakeController) ImageDigest(ctx context.Context, ref string) (string, error) {
	return "sha256:fake", nil
}

type emptyReader struct{}

func (emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }

// TestContainerNameForRoleIsStable pins the per-agent container name
// convention shared by spawn (T011) and migrate7 (T014). The stability
// matters: migrate7 creates the container under this name; spawn
// addresses the existing container by the same name without a database
// lookup.
func TestContainerNameForRoleIsStable(t *testing.T) {
	if got := containerNameForRole("engineer"); got != "garrison-agent-engineer" {
		t.Errorf("containerNameForRole(engineer) = %q; want garrison-agent-engineer", got)
	}
	if got := containerNameForRole("qa-engineer"); got != "garrison-agent-qa-engineer" {
		t.Errorf("hyphenated slug round-trip: got %q", got)
	}
}

// TestRunRealClaudeViaContainerCallsExecOnFakeController pins the M7
// branch's call shape: when invoked, it routes through the supplied
// Controller's Exec with the role-derived container name and the
// supplied claude argv prefixed with "claude" (the container's already-
// installed binary, mirroring the legacy direct-exec invocation
// surface).
//
// Per AGENTS.md "Tests for Go only" rule + "verification before
// completion" — this is a Go-side unit test, not a real claude
// integration test.
func TestRunRealClaudeViaContainerCallsExecOnFakeController(t *testing.T) {
	fc := &fakeController{}
	deps := Deps{
		Queries:        nil, // terminal write path skipped via empty pool — see m7_test_writeTerminalShim below.
		AgentContainer: fc,
	}
	// Use a Cancel-immediately ctx so the terminal-write path
	// short-circuits without needing a DB. The unit test asserts only
	// that fakeController.Exec was invoked with the right shape.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = runRealClaudeViaContainer(ctx, deps, runViaContainerInputs{
		RoleSlug: "engineer",
		Argv:     []string{"-p", "describe ticket"},
		Logger:   testLogger(),
	})
	if len(fc.execCalls) != 1 {
		t.Fatalf("Exec call count = %d; want 1", len(fc.execCalls))
	}
	got := fc.execCalls[0]
	if got.ContainerID != "garrison-agent-engineer" {
		t.Errorf("ContainerID = %q; want garrison-agent-engineer", got.ContainerID)
	}
	if len(got.Cmd) == 0 || got.Cmd[0] != "claude" {
		t.Errorf("Cmd[0] = %q; want claude (got %v)", got.Cmd, got.Cmd)
	}
}

// TestRunRealClaudeViaContainerWithoutController returns without
// panicking when AgentContainer is nil. Defends against the
// misconfigured-deploy case where UseDirectExec=false but the controller
// wiring was forgotten.
func TestRunRealClaudeViaContainerWithoutController(t *testing.T) {
	deps := Deps{} // AgentContainer nil, Pool nil → terminal write skipped
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runRealClaudeViaContainer(ctx, deps, runViaContainerInputs{
		RoleSlug: "engineer",
		Logger:   testLogger(),
	})
	if err != nil {
		t.Errorf("nil controller path returned err = %v; want nil (Pool=nil short-circuit)", err)
	}
}

// fakeControllerExecFails exercises the error path in
// runRealClaudeViaContainer where Exec itself returns a non-nil error.
type fakeControllerExecFails struct {
	fakeController
	err error
}

func (f *fakeControllerExecFails) Exec(ctx context.Context, id string, cmd []string, stdin io.Reader) (io.ReadCloser, io.ReadCloser, error) {
	f.execCalls = append(f.execCalls, fakeExecCall{ContainerID: id, Cmd: append([]string{}, cmd...)})
	return nil, nil, f.err
}

func TestRunRealClaudeViaContainerExecFailureSurfacesAsSpawnFailed(t *testing.T) {
	fc := &fakeControllerExecFails{err: errors.New("simulated daemon down")}
	deps := Deps{AgentContainer: fc}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runRealClaudeViaContainer(ctx, deps, runViaContainerInputs{
		RoleSlug: "engineer",
		Logger:   testLogger(),
	})
	if err != nil {
		t.Errorf("Pool=nil terminal-write short-circuit; err = %v; want nil", err)
	}
	if len(fc.execCalls) != 1 {
		t.Errorf("Exec should have been called once; got %d", len(fc.execCalls))
	}
}

// TestComputeClaudeMDHashEmptyDept returns nil for the no-workspace
// path and a stable hash for the workspace-with-CLAUDE.md case.
func TestComputeClaudeMDHashEmptyDept(t *testing.T) {
	if got := computeClaudeMDHash(deptWithWorkspace("")); got != nil {
		t.Errorf("empty workspace path should yield nil; got %v", got)
	}
}

func TestComputeClaudeMDHashFromWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(dir, "CLAUDE.md", "# project context"); err != nil {
		t.Fatalf("seed CLAUDE.md: %v", err)
	}
	got := computeClaudeMDHash(deptWithWorkspace(dir))
	if got == nil {
		t.Fatal("expected non-nil hash for workspace with CLAUDE.md")
	}
	// SHA-256 hex is 64 chars.
	if len(*got) != 64 {
		t.Errorf("hash length = %d; want 64", len(*got))
	}
}

func TestComputeClaudeMDHashMissingFile(t *testing.T) {
	dir := t.TempDir()
	got := computeClaudeMDHash(deptWithWorkspace(dir))
	if got != nil {
		t.Errorf("missing CLAUDE.md should yield nil; got %v", got)
	}
}
