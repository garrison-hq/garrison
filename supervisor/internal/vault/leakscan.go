package vault

import "strings"

// RuleOneLeakScan substring-searches agentMD for every value in grantSet.
// Returns a non-empty list of env_var_names whose literal secret value was
// found as a substring; empty or nil means no leak. Per FR-407 and the
// Session 2026-04-24 clarification: scan is per-spawn, not cached.
//
// UnsafeBytes is called here intentionally — this is one of the two call
// sites that reach raw secret bytes (the other is spawn-path env-var
// injection). The vaultlog vet analyzer whitelists this call via the
// in-file nolint directive below.
//
// Empty() secrets are skipped to prevent spurious matches on empty strings
// (every string "contains" the empty string).
func RuleOneLeakScan(agentMD string, grantSet map[string]SecretValue) (leaked []string) {
	for envVar, val := range grantSet {
		if val.Empty() {
			continue
		}
		//nolint:vaultlog // Rule 1 leak scan requires literal substring match on raw bytes
		if strings.Contains(agentMD, string(val.UnsafeBytes())) {
			leaked = append(leaked, envVar)
		}
	}
	return
}
