package spawn

// Unit tests for the M9 T007 oneshot MCP-config builder — the
// finalize-mode env seam (plan §2 step 4): the finalize entry carries
// GARRISON_FINALIZE_MODE=oneshot + GARRISON_SCHEDULED_RUN_ID and no
// ticket env; the sealed entry set is otherwise untouched.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpconfig"
	"github.com/jackc/pgx/v5/pgtype"
)

// oneshotTestWriteParams renders the same WriteParams shape the
// container path hands buildOneshotMCPConfig.
func oneshotTestWriteParams(t *testing.T) mcpconfig.WriteParams {
	t.Helper()
	var instanceID pgtype.UUID
	if err := instanceID.Scan("11111111-2222-3333-4444-555555555555"); err != nil {
		t.Fatalf("scan instance uuid: %v", err)
	}
	return mcpconfig.WriteParams{
		InstanceID:    instanceID,
		SupervisorBin: "/usr/local/bin/garrison-supervisor",
		DSN:           "postgres://agent_ro@localhost/garrison",
		Finalize: mcpconfig.FinalizeParams{
			SupervisorBin:   "/usr/local/bin/garrison-supervisor",
			AgentInstanceID: "11111111-2222-3333-4444-555555555555",
			DatabaseURL:     "postgres://agent_ro@localhost/garrison",
		},
		GarrisonMutate: mcpconfig.GarrisonMutateParams{
			SupervisorBin:   "/usr/local/bin/garrison-supervisor",
			AgentInstanceID: "11111111-2222-3333-4444-555555555555",
			DatabaseURL:     "postgres://supervisor@localhost/garrison",
		},
		OmitMempalace: true,
	}
}

type oneshotTestServerSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func decodeMCPConfig(t *testing.T, data []byte) map[string]oneshotTestServerSpec {
	t.Helper()
	var cfg struct {
		MCPServers map[string]oneshotTestServerSpec `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}
	return cfg.MCPServers
}

// TestOneshotMCPConfigCarriesModeEnvs pins the T007 completion
// condition: the oneshot config builder's finalize entry carries both
// mode envs and no ticket env, while the entry set and the
// instance-scoping env (required by runMCPFinalize in every mode)
// survive unchanged.
func TestOneshotMCPConfigCarriesModeEnvs(t *testing.T) {
	const runID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	data, fileName, err := buildOneshotMCPConfig(oneshotTestWriteParams(t), runID)
	if err != nil {
		t.Fatalf("buildOneshotMCPConfig: %v", err)
	}
	if want := "mcp-config-11111111-2222-3333-4444-555555555555.json"; fileName != want {
		t.Errorf("fileName = %q; want %q", fileName, want)
	}

	servers := decodeMCPConfig(t, data)
	fin, ok := servers["finalize"]
	if !ok {
		t.Fatalf("rendered config has no finalize entry; servers: %v", servers)
	}
	if got := fin.Env["GARRISON_FINALIZE_MODE"]; got != "oneshot" {
		t.Errorf("finalize env GARRISON_FINALIZE_MODE = %q; want %q", got, "oneshot")
	}
	if got := fin.Env["GARRISON_SCHEDULED_RUN_ID"]; got != runID {
		t.Errorf("finalize env GARRISON_SCHEDULED_RUN_ID = %q; want %q", got, runID)
	}
	// runMCPFinalize requires the instance scoping env in every mode.
	if got := fin.Env["GARRISON_AGENT_INSTANCE_ID"]; got != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("finalize env GARRISON_AGENT_INSTANCE_ID = %q; want instance uuid", got)
	}
	// No ticket env anywhere in the config (oneshot spawns have no ticket).
	for name, spec := range servers {
		for k := range spec.Env {
			if strings.Contains(k, "TICKET") {
				t.Errorf("server %q carries ticket env %q; oneshot configs must not", name, k)
			}
		}
	}

	// Regression pin (FR-302 posture): the ticket-path render carries
	// no mode env — GARRISON_FINALIZE_MODE defaults to ticket in
	// runMCPFinalize, keeping ticket configs byte-for-byte unchanged.
	ticketData, _, err := mcpconfig.Render(oneshotTestWriteParams(t))
	if err != nil {
		t.Fatalf("mcpconfig.Render (ticket shape): %v", err)
	}
	ticketServers := decodeMCPConfig(t, ticketData)
	if _, present := ticketServers["finalize"].Env["GARRISON_FINALIZE_MODE"]; present {
		t.Error("ticket-path finalize entry carries GARRISON_FINALIZE_MODE; must not")
	}
	if _, present := ticketServers["finalize"].Env["GARRISON_SCHEDULED_RUN_ID"]; present {
		t.Error("ticket-path finalize entry carries GARRISON_SCHEDULED_RUN_ID; must not")
	}
}

// TestInjectOneshotFinalizeEnvRequiresFinalizeEntry pins the seam's
// fail-closed posture: a config without a finalize entry cannot be
// annotated into an oneshot config.
func TestInjectOneshotFinalizeEnvRequiresFinalizeEntry(t *testing.T) {
	p := oneshotTestWriteParams(t)
	p.Finalize = mcpconfig.FinalizeParams{} // disabled → no finalize entry rendered
	data, _, err := mcpconfig.Render(p)
	if err != nil {
		t.Fatalf("mcpconfig.Render: %v", err)
	}
	if _, err := injectOneshotFinalizeEnv(data, "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"); err == nil {
		t.Fatal("injectOneshotFinalizeEnv accepted a config without a finalize entry; want error")
	}
}
