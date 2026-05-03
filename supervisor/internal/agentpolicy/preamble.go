// Package agentpolicy carries the immutable security preamble Garrison
// prepends to every agent's system prompt. The preamble is operator-
// controlled and edited via PR + code review only; the byte-equality
// test in this package's preamble_test.go pins the const against
// preamble.go.golden so future edits show up in code review.
//
// The preamble is policy-style (directive: "Garrison agents: X is
// prohibited") rather than identity-style ("You are X"). Identity-style
// content trips Claude's built-in prompt-injection detection
// (docs/research/m7-spike.md §8 P9); the byte-equality test plus
// TestPreambleHasNoIdentityAssertion guard against regression.
//
// See docs/security/agent-sandbox-threat-model.md Rule 9 and
// docs/security/hiring-threat-model.md Rule 8 for the binding
// rationale.
package agentpolicy

import (
	"crypto/sha256"
	// _ "embed" enables the //go:embed directive on preambleBody below;
	// no symbols are referenced from the package directly.
	_ "embed"
	"encoding/hex"
)

//go:embed preamble.md
var preambleBody string

// preambleHash is the hex-encoded SHA-256 of preambleBody, computed at
// package init. Recorded on every agent_instances row at spawn time
// (FR-304) so a forensic query can reconstruct exactly which preamble
// version was active for any historical run.
var preambleHash = func() string {
	sum := sha256.Sum256([]byte(preambleBody))
	return hex.EncodeToString(sum[:])
}()

// Body returns the preamble's exact bytes. Read by ComposeSystemPrompt
// to prepend the preamble above agent.md content (FR-303).
func Body() string { return preambleBody }

// Hash returns the hex-encoded SHA-256 of the preamble body. Stable
// across calls within a supervisor process.
func Hash() string { return preambleHash }
