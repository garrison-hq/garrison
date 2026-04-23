//go:build live_acceptance

// M2.2.2 T012 — compliance matrix test (SC-311).
// TestM222ComplianceMatrix runs the happy-path scenario 3 times
// against haiku-4-5-20251001 and 3 times against claude-opus-4-7.
// Postgres-only assertions per Clarification 2026-04-23 Q4; palace
// inspection is operator-manual in the retro (T014).
//
// Build-tag-gated (live_acceptance) so this only runs on explicit
// operator invocation with real Anthropic credentials. Expected
// spend: under $3 USD across all 6 runs per SC-311 headline.
//
// Run:
//   go test -tags=live_acceptance -count=1 -timeout=45m \
//           -run=TestM222ComplianceMatrix .
//
// Requires:
//   - real `claude` binary on $PATH (2.1.117 or compatible)
//   - spike-mempalace + spike-docker-proxy containers running
//   - Anthropic credentials configured for the claude binary

package supervisor_test

import (
	"os/exec"
	"testing"
)

// TestM222ComplianceMatrix — SC-311. 3 runs per model × 2 models =
// 6 total iterations. Aggregate assertions:
//   - ≥ 2 of 3 runs per model produce hygiene_status='clean' on
//     both transitions
//   - combined cost across all 6 runs < $3.00 USD
//
// Per-iteration t.Logf emits cost + transition details to aid manual
// retro authoring; palace filesystem introspection is NOT part of
// the test body (Clarification Q4).
func TestM222ComplianceMatrix(t *testing.T) {
	requireSpikeStack(t)

	realClaude, err := exec.LookPath("claude")
	if err != nil {
		t.Skipf("claude not on PATH: %v", err)
	}

	const runsPerModel = 3
	models := []string{
		"claude-haiku-4-5-20251001",
		"claude-opus-4-7",
	}

	// Per-model aggregation.
	type modelAgg struct {
		cleanRuns int
		totalRuns int
		cost      float64
	}
	perModel := make(map[string]*modelAgg, len(models))
	var grandCost float64

	for _, model := range models {
		perModel[model] = &modelAgg{}
		for i := 0; i < runsPerModel; i++ {
			iterName := iterLabel(model, i+1)
			t.Run(iterName, func(t *testing.T) {
				res := runComplianceOnce(t, realClaude, model)
				agg := perModel[model]
				agg.totalRuns++
				agg.cost += res.CombinedCostUSD
				grandCost += res.CombinedCostUSD

				clean := isCleanRun(res)
				if clean {
					agg.cleanRuns++
				}

				t.Logf("[M222-matrix] model=%s iter=%d clean=%v hygiene=%v exit=%v cost=$%.4f err=%q",
					model, i+1, clean, res.HygieneStatuses, res.ExitReasons,
					res.CombinedCostUSD, res.Error)
			})
		}
	}

	// Aggregate SC-311 assertions.
	for _, model := range models {
		agg := perModel[model]
		if agg.cleanRuns < 2 {
			t.Errorf("SC-311: %s had %d/%d clean runs; want at least 2/3",
				model, agg.cleanRuns, agg.totalRuns)
		}
		t.Logf("[M222-matrix-summary] %s: clean=%d/%d cost=$%.4f",
			model, agg.cleanRuns, agg.totalRuns, agg.cost)
	}

	if grandCost >= 3.0 {
		t.Errorf("SC-311: combined cost across 6 runs = $%.4f; want strictly < $3.00",
			grandCost)
	}
	t.Logf("[M222-matrix-grand] total_cost=$%.4f (SC-311 cap $3.00)", grandCost)
}

// isCleanRun returns true when the run produced 2 agent_instances
// rows (engineer + qa-engineer) AND both observed transition rows
// carry hygiene_status='clean'. Anything else — partial rows, missing
// transitions, non-clean hygiene — fails the per-run bar.
func isCleanRun(res runResult) bool {
	if res.Error != "" {
		return false
	}
	if res.AgentInstances != 2 {
		return false
	}
	if len(res.HygieneStatuses) < 2 {
		return false
	}
	for _, s := range res.HygieneStatuses {
		if s != "clean" {
			return false
		}
	}
	return true
}

// iterLabel makes a t.Run name that's both model-recognisable and
// ASCII-safe (Go's subtest filter treats spaces and "/" specially).
func iterLabel(model string, iter int) string {
	return model + "_run" + itoa(iter)
}

// itoa avoids importing strconv for a one-digit conversion.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return "X" // unreachable for runsPerModel=3
}
