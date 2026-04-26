package vault

import (
	"testing"
)

func TestRuleOneLeakScanEmptyGrantSet(t *testing.T) {
	leaked := RuleOneLeakScan("some agent.md content", nil)
	if leaked != nil {
		t.Errorf("expected nil for empty grant set, got %v", leaked)
	}
	leaked = RuleOneLeakScan("some agent.md content", map[string]SecretValue{})
	if leaked != nil {
		t.Errorf("expected nil for empty grant map, got %v", leaked)
	}
}

func TestRuleOneLeakScanPositiveMatch(t *testing.T) {
	secretVal := "sk-test-realvalue123456789012345"
	agentMD := "Use the API key: " + secretVal + " for authentication."
	grants := map[string]SecretValue{
		"EXAMPLE_API_KEY": New([]byte(secretVal)),
	}
	leaked := RuleOneLeakScan(agentMD, grants)
	if len(leaked) == 0 {
		t.Error("expected EXAMPLE_API_KEY in leaked, got none")
	}
	if leaked[0] != "EXAMPLE_API_KEY" {
		t.Errorf("expected leaked[0]=%q, got %q", "EXAMPLE_API_KEY", leaked[0])
	}
}

func TestRuleOneLeakScanEnvVarNameOnly(t *testing.T) {
	agentMD := "Pass $EXAMPLE_API_KEY to the service, do not log it."
	grants := map[string]SecretValue{
		"EXAMPLE_API_KEY": New([]byte("sk-actualsecretvalue12345678901")),
	}
	leaked := RuleOneLeakScan(agentMD, grants)
	if leaked != nil {
		t.Errorf("env-var name reference should not match; got leaked=%v", leaked)
	}
}

func TestRuleOneLeakScanMultipleSecretsOneMatch(t *testing.T) {
	secret2Val := "test-slack-token-fake-aabbccddee1122"
	agentMD := "Workspace note. Token: " + secret2Val
	grants := map[string]SecretValue{
		"STRIPE_KEY":  New([]byte("sk-stripe-notpresentinagentmd1234")),
		"SLACK_TOKEN": New([]byte(secret2Val)),
		"GH_PAT":      New([]byte("ghp_notpresentinagentmd1234567890123")),
	}
	leaked := RuleOneLeakScan(agentMD, grants)
	if len(leaked) != 1 {
		t.Fatalf("expected exactly 1 leaked env var, got %d: %v", len(leaked), leaked)
	}
	if leaked[0] != "SLACK_TOKEN" {
		t.Errorf("expected SLACK_TOKEN, got %q", leaked[0])
	}
}

func TestRuleOneLeakScanZeroValueSecretSkipped(t *testing.T) {
	var empty SecretValue // zero value, Empty() == true
	agentMD := "agent description with various text"
	grants := map[string]SecretValue{
		"EMPTY_KEY": empty,
	}
	leaked := RuleOneLeakScan(agentMD, grants)
	if leaked != nil {
		t.Errorf("zero-value secret should be skipped; empty string matches everything, got %v", leaked)
	}
}
