package mempalace

import "testing"

func TestMCPServerSpec(t *testing.T) {
	cfg := SpecConfig{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "tcp://garrison-docker-proxy:2375",
	}
	command, args, env := MCPServerSpec(cfg)

	if command != "/usr/bin/docker" {
		t.Errorf("command=%q; want /usr/bin/docker", command)
	}
	wantArgs := []string{
		"exec", "-i", "garrison-mempalace",
		"python", "-m", "mempalace.mcp_server",
		"--palace", "/palace",
	}
	if !sliceEq(args, wantArgs) {
		t.Errorf("args mismatch\n  got:  %v\n  want: %v", args, wantArgs)
	}
	if got := env["DOCKER_HOST"]; got != "tcp://garrison-docker-proxy:2375" {
		t.Errorf("env.DOCKER_HOST=%q; want tcp://garrison-docker-proxy:2375", got)
	}
	if len(env) != 1 {
		t.Errorf("env has %d keys; want 1 (DOCKER_HOST only)", len(env))
	}
	// --max-tokens must not appear anywhere (T001 finding F2).
	// Nor MEMPALACE_PATH (T001 finding F2: --palace flag carries the
	// path; no env-var mechanism).
	for _, a := range args {
		if a == "--max-tokens" {
			t.Errorf("--max-tokens in args; must not appear: %v", args)
		}
	}
	if _, ok := env["MEMPALACE_PATH"]; ok {
		t.Errorf("MEMPALACE_PATH in env; must not appear (T001 F2)")
	}
}
