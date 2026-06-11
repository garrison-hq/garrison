package agentcontainer

import "testing"

// TestContainerNameUsesShortAgentID pins FR-008: the canonical
// per-agent container name is garrison-agent-<8-char short id> — the
// same convention migrate7 already creates, now the single naming
// source for the spawn-side lookup too.
func TestContainerNameUsesShortAgentID(t *testing.T) {
	got := ContainerName("11112222-3333-4444-5555-666677778888")
	if got != "garrison-agent-11112222" {
		t.Errorf("ContainerName = %q; want garrison-agent-11112222", got)
	}
}

// TestSpecForAgentKeysWorkspaceByAgentUUID pins FR-006: the workspace
// host dir is keyed by the full agent UUID, not the role slug — two
// agents sharing a role never share a scratch dir. Skills keep their
// role-slug keying (unchanged from M7), and the network + supervisor
// binary values thread through.
func TestSpecForAgentKeysWorkspaceByAgentUUID(t *testing.T) {
	spec := SpecForAgent(AgentSpecParams{
		AgentID:       "11112222-3333-4444-5555-666677778888",
		RoleSlug:      "engineer",
		ImageDigest:   "garrison-claude@sha256:deadbeef",
		HostUID:       1042,
		WorkspaceFS:   "/var/lib/garrison/workspaces",
		SkillsFS:      "/var/lib/garrison/skills",
		NetworkName:   "garrison-agents",
		SupervisorBin: "/var/lib/garrison/shared/garrison-supervisor",
		Memory:        "512m",
		CPUs:          "1.0",
		PIDsLimit:     200,
	})

	if want := "/var/lib/garrison/workspaces/11112222-3333-4444-5555-666677778888"; spec.Workspace != want {
		t.Errorf("Workspace = %q; want %q (FR-006: agent-UUID keying)", spec.Workspace, want)
	}
	if want := "/var/lib/garrison/skills/engineer"; spec.Skills != want {
		t.Errorf("Skills = %q; want %q (role-slug keying unchanged)", spec.Skills, want)
	}
	if spec.AgentID != "11112222-3333-4444-5555-666677778888" {
		t.Errorf("AgentID = %q; want the full UUID", spec.AgentID)
	}
	if spec.Image != "garrison-claude@sha256:deadbeef" {
		t.Errorf("Image = %q; want the pinned digest", spec.Image)
	}
	if spec.HostUID != 1042 {
		t.Errorf("HostUID = %d; want 1042", spec.HostUID)
	}
	if spec.NetworkName != "garrison-agents" {
		t.Errorf("NetworkName = %q; want garrison-agents", spec.NetworkName)
	}
	if spec.SupervisorBin != "/var/lib/garrison/shared/garrison-supervisor" {
		t.Errorf("SupervisorBin = %q; want the host binary path", spec.SupervisorBin)
	}
	if spec.Memory != "512m" || spec.CPUs != "1.0" || spec.PIDsLimit != 200 {
		t.Errorf("resource caps = %s/%s/%d; want 512m/1.0/200", spec.Memory, spec.CPUs, spec.PIDsLimit)
	}
}
