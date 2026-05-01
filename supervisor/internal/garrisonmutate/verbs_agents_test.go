package garrisonmutate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestPauseAgentRejectsMissingRoleSlug pins FR-414 validation.
func TestPauseAgentRejectsMissingRoleSlug(t *testing.T) {
	expectValidationFailure(t, realPauseAgentHandler, `{}`, "agent_role_slug is required")
}

// TestResumeAgentRejectsMissingRoleSlug pins FR-414 validation.
func TestResumeAgentRejectsMissingRoleSlug(t *testing.T) {
	expectValidationFailure(t, realResumeAgentHandler, `{}`, "agent_role_slug is required")
}

// TestSpawnAgentRequiresRoleSlugAndTicketID covers spawn_agent
// validation.
func TestSpawnAgentRequiresRoleSlugAndTicketID(t *testing.T) {
	expectValidationFailure(t, realSpawnAgentHandler, `{}`, "agent_role_slug is required")
	expectValidationFailure(t, realSpawnAgentHandler,
		`{"agent_role_slug":"engineer"}`, "ticket_id is required")
}

// TestEditAgentConfigRequiresAtLeastOneField covers edit_agent_config
// validation: at-least-one-field-required (avoids no-op masquerades).
func TestEditAgentConfigRequiresAtLeastOneField(t *testing.T) {
	expectValidationFailure(t, realEditAgentConfigHandler,
		`{"agent_role_slug":"engineer"}`,
		"at least one of",
	)
}

// TestEditAgentConfigLeakScanFiresOnPlantedSecret covers FR-421's
// pre-tx leak-scan: a planted sk- value triggers ErrLeakScanFailed
// without touching the DB. Validates the regex-based scanner without
// needing a Postgres testcontainer (Pool stays nil because the leak
// scan runs before any pool call).
func TestEditAgentConfigLeakScanFiresOnPlantedSecret(t *testing.T) {
	plantedAgentMD := "You are an engineer. Use sk-test-FAKE-NOT-A-REAL-KEY-abcdef0123456789 for the API."
	body, _ := json.Marshal(map[string]string{
		"agent_role_slug": "engineer",
		"agent_md":        plantedAgentMD,
	})
	r, err := realEditAgentConfigHandler(context.Background(), validationDeps(), json.RawMessage(body))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if r.Success {
		t.Errorf("expected leak-scan failure; got Success=true")
	}
	if r.ErrorKind != string(ErrLeakScanFailed) {
		t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrLeakScanFailed)
	}
	if !strings.Contains(r.Message, "secret-shaped") {
		t.Errorf("Message %q missing 'secret-shaped'", r.Message)
	}
}

// TestEditAgentConfigLeakScanPassesOnCleanInput verifies the leak scan
// returns no hits for clean strings. (Past the leak scan, the verb
// hits the DB which is nil in this test, returning a count error —
// we assert only the leak-scan branch.)
func TestEditAgentConfigLeakScanPassesOnCleanInput(t *testing.T) {
	clean := "You are an engineer. Use the env var $STRIPE_KEY to make payments."
	hits := scanForSecrets(clean)
	if len(hits) != 0 {
		t.Errorf("scanForSecrets on clean input returned %d hits; want 0", hits)
	}
}

// TestScanForSecretsCatchesAllPatterns asserts each registered pattern
// fires on a known-positive input. Defense-in-depth against pattern
// regressions during refactors.
func TestScanForSecretsCatchesAllPatterns(t *testing.T) {
	cases := []string{
		"sk-test-FAKE-NOT-A-REAL-KEY-abcdef0123456789",
		"xoxb-FAKE-TESTONLY-NOT-A-REAL-SLACK-VALUE",
		"AKIAFAKETESTONLYABCD",
		"-----BEGIN RSA PRIVATE KEY-----",
		"ghp_FAKETESTONLYNOTAREALPATABCDEF0123456789",
		"ghs_FAKETESTONLYNOTAREALPATABCDEF0123456789",
		"gho_FAKETESTONLYNOTAREALPATABCDEF0123456789",
		"ghr_FAKETESTONLYNOTAREALPATABCDEF0123456789",
		"ghu_FAKETESTONLYNOTAREALPATABCDEF0123456789",
		"Bearer FAKE-TESTONLY-NOT-A-REAL-BEARER-VALUE-1234",
	}
	for _, c := range cases {
		hits := scanForSecrets(c)
		if len(hits) == 0 {
			t.Errorf("scanForSecrets(%q) returned 0 hits; expected at least 1", c)
		}
	}
}

// TestAgentRegistryRealHandlers verifies the per-domain init() ran and
// replaced the stubs.
func TestAgentRegistryRealHandlers(t *testing.T) {
	for _, name := range []string{"pause_agent", "resume_agent", "spawn_agent", "edit_agent_config"} {
		v := FindVerb(name)
		if v == nil {
			t.Errorf("FindVerb(%q) = nil", name)
			continue
		}
		r, _ := v.Handler(context.Background(), validationDeps(), json.RawMessage(`{not json`))
		if strings.Contains(r.Message, "not yet implemented") {
			t.Errorf("verb %q still using stubHandler", name)
		}
	}
}
