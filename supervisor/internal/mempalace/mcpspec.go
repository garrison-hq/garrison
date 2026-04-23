package mempalace

// SpecConfig carries the four values that define the MCP-config `mempalace`
// entry. DockerBin is the absolute path to the docker CLI inside the
// supervisor container (typically "/usr/bin/docker", resolved at config-
// load time via exec.LookPath). MempalaceContainer is the sidecar's
// container name (compose default: "garrison-mempalace"). PalacePath is
// the path inside the sidecar where the palace volume is mounted (compose
// default: "/palace"). DockerHost is the TCP URL of the filtered docker-
// proxy endpoint on the compose network (compose default:
// "tcp://garrison-docker-proxy:2375" — T001 finding F5 corrected the
// unix-socket assumption).
type SpecConfig struct {
	DockerBin          string
	MempalaceContainer string
	PalacePath         string
	DockerHost         string
}

// MCPServerSpec builds the MCP-config entry the supervisor writes into
// the per-invocation config file under the `mempalace` key. Shape per
// plan §"MCP config extension":
//
//	command: <DockerBin>
//	args:    ["exec", "-i", <MempalaceContainer>,
//	          "python", "-m", "mempalace.mcp_server", "--palace", <PalacePath>]
//	env:     {"DOCKER_HOST": <DockerHost>}
//
// mcpconfig.Write calls this and pastes the returned triple into the
// JSON emitted for Claude's --mcp-config file.
//
// Note the `python -m mempalace.mcp_server` form: T001 finding F1
// superseded Session 2026-04-22 Q1 — `mempalace mcp` is a help-text
// printer in 3.3.2, not a server. The authoritative stdio MCP server
// is the Python module entry.
func MCPServerSpec(cfg SpecConfig) (command string, args []string, env map[string]string) {
	command = cfg.DockerBin
	args = []string{
		"exec", "-i", cfg.MempalaceContainer,
		"python", "-m", "mempalace.mcp_server",
		"--palace", cfg.PalacePath,
	}
	env = map[string]string{
		"DOCKER_HOST": cfg.DockerHost,
	}
	return
}
