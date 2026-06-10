package agentcontainer

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBuildCreateBodyIdleEntrypointTmpfsNetwork pins the M7.1 create
// shape (FR-005): idle `/bin/sleep infinity` entrypoint (spike F1 —
// no more Exited(1) fleet), the `/home/node` HOME tmpfs alongside
// `/tmp` (spike F5), NetworkMode from spec.NetworkName, and the
// sealed M7 caps unchanged (ReadonlyRootfs, CapDrop ALL).
func TestBuildCreateBodyIdleEntrypointTmpfsNetwork(t *testing.T) {
	spec := validSpec()
	spec.NetworkName = "garrison-agents"

	body, err := buildCreateBody(spec)
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}

	if len(body.Entrypoint) != 1 || body.Entrypoint[0] != "/bin/sleep" {
		t.Errorf("Entrypoint = %v; want [/bin/sleep] (FR-005)", body.Entrypoint)
	}
	if len(body.Cmd) != 1 || body.Cmd[0] != "infinity" {
		t.Errorf("Cmd = %v; want [infinity] (FR-005)", body.Cmd)
	}
	if got, want := body.HostConfig.Tmpfs["/tmp"], "rw,size=64m"; got != want {
		t.Errorf("Tmpfs[/tmp] = %q; want %q", got, want)
	}
	if got, want := body.HostConfig.Tmpfs["/home/node"], "rw,size=64m"; got != want {
		t.Errorf("Tmpfs[/home/node] = %q; want %q (spike F5)", got, want)
	}
	if body.HostConfig.NetworkMode != "garrison-agents" {
		t.Errorf("NetworkMode = %q; want garrison-agents (FR-012)", body.HostConfig.NetworkMode)
	}
	if !body.HostConfig.ReadonlyRootfs {
		t.Errorf("ReadonlyRootfs = false; sealed M7 surface violated")
	}
	if len(body.HostConfig.CapDrop) != 1 || body.HostConfig.CapDrop[0] != "ALL" {
		t.Errorf("CapDrop = %v; want [ALL] (sealed M7 surface)", body.HostConfig.CapDrop)
	}
}

// TestBuildCreateBodyMountsSupervisorBinaryReadOnly pins the FR-014
// bind: the host supervisor binary mounted read-only at
// /usr/local/bin/garrison-supervisor (spike F6) so in-container stdio
// MCP servers run from it. Absent SupervisorBin (pre-T006 migrate7
// args) adds no bind.
func TestBuildCreateBodyMountsSupervisorBinaryReadOnly(t *testing.T) {
	spec := validSpec()
	spec.SupervisorBin = "/var/lib/garrison/shared/garrison-supervisor"

	body, err := buildCreateBody(spec)
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}
	want := "/var/lib/garrison/shared/garrison-supervisor:/usr/local/bin/garrison-supervisor:ro"
	found := false
	for _, b := range body.HostConfig.Binds {
		if b == want {
			found = true
		}
	}
	if !found {
		t.Errorf("Binds = %v; want a %q entry (FR-014)", body.HostConfig.Binds, want)
	}

	noBin, err := buildCreateBody(validSpec())
	if err != nil {
		t.Fatalf("buildCreateBody (no SupervisorBin): %v", err)
	}
	for _, b := range noBin.HostConfig.Binds {
		if strings.Contains(b, "garrison-supervisor") {
			t.Errorf("Binds = %v; empty SupervisorBin must add no bind", noBin.HostConfig.Binds)
		}
	}
}

// TestBuildCreateBodyOmitsContainerLevelEnv pins FR-002 structurally:
// the create body carries no container-level Env at all — per-exec
// ExecSpec.Env is the only secret/runtime-env transit.
func TestBuildCreateBodyOmitsContainerLevelEnv(t *testing.T) {
	body, err := buildCreateBody(validSpec())
	if err != nil {
		t.Fatalf("buildCreateBody: %v", err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := asMap["Env"]; ok {
		t.Errorf("create body carries a top-level Env key; FR-002 violated: %s", raw)
	}
}

// TestShapeHashDeterministicAndFieldSensitive pins FR-007: the
// garrison.shape_hash label is the hex SHA-256 of the marshaled body
// — identical for identical specs, different when any per-agent field
// changes.
func TestShapeHashDeterministicAndFieldSensitive(t *testing.T) {
	base := validSpec()
	base.NetworkName = "garrison-agents"
	base.SupervisorBin = "/var/lib/garrison/shared/garrison-supervisor"

	hashOf := func(t *testing.T, spec ContainerSpec) string {
		t.Helper()
		body, err := buildCreateBody(spec)
		if err != nil {
			t.Fatalf("buildCreateBody: %v", err)
		}
		h := body.Labels[shapeHashLabel]
		if len(h) != 64 || strings.ToLower(h) != h {
			t.Fatalf("shape hash %q; want 64 lower-case hex chars", h)
		}
		return h
	}

	baseHash := hashOf(t, base)
	if again := hashOf(t, base); again != baseHash {
		t.Errorf("same spec hashed differently: %s vs %s", baseHash, again)
	}

	mutations := map[string]func(*ContainerSpec){
		"Image":         func(s *ContainerSpec) { s.Image = "garrison-claude@sha256:other" },
		"HostUID":       func(s *ContainerSpec) { s.HostUID = 1043 },
		"Workspace":     func(s *ContainerSpec) { s.Workspace = "/var/lib/garrison/workspaces/other" },
		"Skills":        func(s *ContainerSpec) { s.Skills = "/var/lib/garrison/skills/other" },
		"NetworkName":   func(s *ContainerSpec) { s.NetworkName = "garrison-agents-2" },
		"SupervisorBin": func(s *ContainerSpec) { s.SupervisorBin = "/elsewhere/garrison-supervisor" },
		"Memory":        func(s *ContainerSpec) { s.Memory = "1g" },
		"CPUs":          func(s *ContainerSpec) { s.CPUs = "0.5" },
		"PIDsLimit":     func(s *ContainerSpec) { s.PIDsLimit = 300 },
		"AgentID":       func(s *ContainerSpec) { s.AgentID = "99990000-1111-2222-3333-444455556666" },
	}
	for field, mutate := range mutations {
		spec := base
		mutate(&spec)
		if got := hashOf(t, spec); got == baseHash {
			t.Errorf("mutating %s did not change the shape hash (FR-007)", field)
		}
	}
}
