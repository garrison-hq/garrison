package agentcontainer

// ContainerName is the canonical per-agent container name —
// "garrison-agent-<short-id>" (decision #32; FR-008). The single
// naming source for create-side callers (migrate7, skillinstall, the
// boot shape reconcile) and the spawn-side lookup; spawn's pre-M7.1
// role-keyed name was the acceptance-diary latent bug.
func ContainerName(agentID string) string {
	return "garrison-agent-" + shortID(agentID)
}

// AgentSpecParams is the input to SpecForAgent: the per-agent column
// values (agents row) plus the host-side config values main threads
// through migrate7, skillinstall, and the boot reconcile.
type AgentSpecParams struct {
	AgentID       string // full agent UUID — keys the workspace dir + container name
	RoleSlug      string // skills keying only (unchanged from M7)
	ImageDigest   string // pinned digest from Controller.ImageDigest
	HostUID       int    // FR-206a allocator output
	WorkspaceFS   string // host base dir; workspace = <WorkspaceFS>/<agent-uuid>
	SkillsFS      string // host base dir; skills = <SkillsFS>/<role-slug>
	NetworkName   string // the agents network (FR-012)
	SupervisorBin string // host path of the supervisor binary (FR-014)
	Memory        string // "512m"
	CPUs          string // "1.0"
	PIDsLimit     int    // 200
}

// SpecForAgent builds the per-agent ContainerSpec — the single spec
// source every create-side caller goes through (plan §2), so all
// containers converge on one shape and one shape hash. The workspace
// is keyed by the full agent UUID (FR-006): two agents sharing a role
// never share a scratch dir.
func SpecForAgent(p AgentSpecParams) ContainerSpec {
	return ContainerSpec{
		AgentID:       p.AgentID,
		Image:         p.ImageDigest,
		HostUID:       p.HostUID,
		Workspace:     p.WorkspaceFS + "/" + p.AgentID,
		Skills:        p.SkillsFS + "/" + p.RoleSlug,
		NetworkName:   p.NetworkName,
		SupervisorBin: p.SupervisorBin,
		Memory:        p.Memory,
		CPUs:          p.CPUs,
		PIDsLimit:     p.PIDsLimit,
	}
}
