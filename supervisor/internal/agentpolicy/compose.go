package agentpolicy

// PrependPreamble returns the operator-controlled security preamble
// followed by a "---" separator and the agent.md content. Used by
// internal/mempalace/wakeup.go::ComposeSystemPrompt at spawn time
// (FR-303). The supervisor passes the result via --append-system-prompt
// to claude-code; the preamble's prompt-position-power above agent.md
// is the structural enforcement that the immutable-preamble threat-
// model rule (sandbox Rule 9, hiring Rule 8) depends on.
func PrependPreamble(agentMD string) string {
	return preambleBody + "\n\n---\n\n" + agentMD
}
