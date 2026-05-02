package agentpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPreambleByteEquality pins preamble.md against preamble.go.golden.
// Any edit to preamble.md without updating .golden trips this test, so
// preamble changes land via PR + code review per FR-306. The .golden
// file is exact-byte mirror, not a pretty-printed render.
func TestPreambleByteEquality(t *testing.T) {
	embedded := []byte(Body())
	golden, err := os.ReadFile(filepath.Join("preamble.go.golden"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	if string(embedded) != string(golden) {
		t.Fatalf("preamble.md and preamble.go.golden differ.\n"+
			"If you edited preamble.md intentionally, mirror the change to "+
			"preamble.go.golden.\nembedded len=%d, golden len=%d",
			len(embedded), len(golden))
	}
}

// TestPreambleHashIsStable confirms Hash() returns a deterministic
// SHA-256 across calls. Used by FR-304 (recorded on every
// agent_instances row); any drift between the const and the recorded
// hash would defeat forensic reconstructability.
func TestPreambleHashIsStable(t *testing.T) {
	a := Hash()
	b := Hash()
	if a != b {
		t.Fatalf("Hash() not stable: %s vs %s", a, b)
	}
	// Spot-check the format: hex-encoded sha256 → 64 hex chars.
	if len(a) != 64 {
		t.Fatalf("Hash() = %q; want 64 hex chars", a)
	}
	// And confirm it actually matches a fresh sha256 of Body().
	want := sha256.Sum256([]byte(Body()))
	if a != hex.EncodeToString(want[:]) {
		t.Fatalf("Hash() = %s; sha256(Body()) = %s",
			a, hex.EncodeToString(want[:]))
	}
}

// TestPreambleHasNoIdentityAssertion is the regression gate for the
// spike §8 P9 finding: --append-system-prompt content phrased as
// identity override ("you are X / your role is Y") is REJECTED by
// Claude's built-in injection detection, regardless of operator
// authorship. The preamble must therefore be policy-style only.
//
// The regex set below is conservative — it catches the most common
// identity-assertion phrasings without being a strict natural-language
// classifier. False positives (operator wants legitimate text the regex
// flags) need a regex update + PR review; false negatives (identity
// phrasing the regex misses) need both a regex update AND an empirical
// re-validation against TestPreambleWinsOverContradictorySkill (T012).
func TestPreambleHasNoIdentityAssertion(t *testing.T) {
	body := strings.ToLower(Body())
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`you are (?:an? )?(?:agent|assistant|claude|garrison)`),
		regexp.MustCompile(`your role is`),
		regexp.MustCompile(`you must identify (?:as|yourself)`),
		regexp.MustCompile(`act as (?:an? )?(?:agent|assistant|claude)`),
		regexp.MustCompile(`pretend (?:to be|you are)`),
	}
	for _, re := range forbidden {
		if loc := re.FindStringIndex(body); loc != nil {
			snippet := body[max0(loc[0]-30):min(len(body), loc[1]+30)]
			t.Errorf("preamble contains identity-assertion phrasing matching %v\n"+
				"  …context: %q\n"+
				"  This trips Claude's prompt-injection refusal (spike §8 P9).\n"+
				"  Reword as a directive ('Garrison agents: X is required') instead.",
				re, snippet)
		}
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// TestComposeSystemPromptPrependsPreamble pins the composer contract:
// the preamble lands first, followed by a "---" separator and the
// agent.md content. The separator is the visual cue an agent reading
// its own system prompt would use to distinguish operator policy from
// role-specific instructions.
func TestComposeSystemPromptPrependsPreamble(t *testing.T) {
	agentMD := "# Engineer\n\nYour responsibilities: …"
	got := PrependPreamble(agentMD)

	if !strings.HasPrefix(got, Body()) {
		t.Errorf("composed prompt does not start with preamble body")
	}
	if !strings.HasSuffix(got, agentMD) {
		t.Errorf("composed prompt does not end with agent.md")
	}
	// Separator between the two layers.
	if !strings.Contains(got, "\n\n---\n\n") {
		t.Errorf("composed prompt missing '---' separator between preamble and agent.md")
	}
	// And the order: preamble must precede agent.md, not the reverse.
	if strings.Index(got, Body()) > strings.Index(got, agentMD) {
		t.Errorf("preamble appears AFTER agent.md in composed prompt; ordering broken")
	}
}
