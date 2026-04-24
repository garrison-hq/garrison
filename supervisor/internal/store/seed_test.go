package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// repoSeedPath returns the absolute path to the committed seed agent.md
// under migrations/seed/. The test file lives under
// supervisor/internal/store/, two levels below supervisor/ and three
// levels below the repo root.
func repoSeedPath(t *testing.T, filename string) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// thisFile = .../supervisor/internal/store/seed_test.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	return filepath.Join(repoRoot, "migrations", "seed", filename)
}

// requiredSections are the 8 headings every M2.2.1 seed agent.md carries
// per T009 completion condition. Each must appear exactly once.
var requiredSections = []string{
	"## Role",
	"## Wake-up context",
	"## Work loop",
	"## Mid-turn MemPalace usage (optional)",
	"## Completion",
	"## Tools available",
	"## What you do not do",
	"## Failure modes",
}

// TestSeedAgentMdStructureAndLength — M2.2.1 T009 structure checks
// plus M2.2.2 T007/T008 rich-structure checks (SC-307). Both seed
// files are (a) between 3500 and 4500 bytes per FR-313; (b) contain
// each required section heading exactly once; (c) mention
// finalize_ticket in the Completion section; (d) do NOT mention
// mempalace_add_drawer or mempalace_kg_add as part of the completion
// protocol (mid-turn section uses is OK); plus the M2.2.2 rich
// structure asserted by requireFinalizeStructure — front-loaded goal,
// one angle-bracket-placeholder example, role-appropriate palace
// calibration bullets, retry framing naming the hint field.
func TestSeedAgentMdStructureAndLength(t *testing.T) {
	for _, name := range []string{"engineer.md", "qa-engineer.md"} {
		t.Run(name, func(t *testing.T) {
			path := repoSeedPath(t, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			body := string(raw)

			// (a) length bounds — widened to [3500, 4500] per FR-313.
			if n := len(body); n < 3500 || n > 4500 {
				t.Errorf("%s size = %d bytes; want in [3500, 4500] per FR-313", name, n)
			}

			// (b) each section heading appears exactly once.
			for _, h := range requiredSections {
				count := strings.Count(body, h)
				if count != 1 {
					t.Errorf("%s: section %q appears %d times; want exactly 1", name, h, count)
				}
			}

			// (c) finalize_ticket must appear in the Completion section.
			completionIdx := strings.Index(body, "## Completion")
			nextSectionIdx := strings.Index(body[completionIdx+1:], "## ")
			completionSection := body[completionIdx : completionIdx+1+nextSectionIdx]
			if !strings.Contains(completionSection, "finalize_ticket") {
				t.Errorf("%s: Completion section does not mention finalize_ticket:\n%s",
					name, completionSection)
			}

			// (d) Completion section must NOT reference mempalace_add_drawer
			// or mempalace_kg_add — those are supervisor-issued from the
			// payload, not agent calls.
			if strings.Contains(completionSection, "mempalace_add_drawer") {
				t.Errorf("%s: Completion section must not reference mempalace_add_drawer",
					name)
			}
			if strings.Contains(completionSection, "mempalace_kg_add") {
				t.Errorf("%s: Completion section must not reference mempalace_kg_add",
					name)
			}

			// M2.2.2 T007/T008 extension: rich structure per SC-307.
			role := "engineer"
			if name == "qa-engineer.md" {
				role = "qa-engineer"
			}
			requireFinalizeStructure(t, body, role)
		})
	}
}

// placeholderRe matches the angle-bracket placeholder syntax the
// example payload uses for agent-filled fields (e.g. <ticket_id>,
// <your one-line outcome>). At least one match must appear inside
// the fenced code block per FR-309 + Clarification Q3.
var placeholderRe = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9 _\-.,'\pP]*>`)

// uuidShapeRe matches realistic UUID shapes (8-4-4-4-12 hex) so the
// test can reject example payloads that use a literal-looking UUID
// instead of an angle-bracket placeholder. Per context §"Implementation
// notes" second bullet, placeholder-copy-verbatim is a real failure
// mode we guard against at seed-file level.
var uuidShapeRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// requireFinalizeStructure — SC-307. Asserts the M2.2.2 rich structure
// any rewritten agent.md must carry:
//   - front-loaded goal sentence in ## Wake-up context (contains both
//     "goal" and "finalize_ticket")
//   - exactly one fenced ```-block with a finalize_ticket payload
//     that contains at least one angle-bracket placeholder and zero
//     realistic-UUID-shaped strings
//   - role-appropriate palace calibration bullets in
//     ## Mid-turn MemPalace usage (optional)
//   - retry framing in ## Failure modes naming the hint field
func requireFinalizeStructure(t *testing.T, body, role string) {
	t.Helper()

	// Front-loaded goal sentence in ## Wake-up context.
	wakeUp := sectionBody(body, "## Wake-up context")
	if !strings.Contains(wakeUp, "goal") || !strings.Contains(wakeUp, "finalize_ticket") {
		t.Errorf("%s: ## Wake-up context must contain a front-loaded sentence with both 'goal' and 'finalize_ticket'; got:\n%s",
			role, wakeUp)
	}

	// Exactly one fenced code block with a finalize_ticket payload
	// carrying angle-bracket placeholders, no realistic UUIDs.
	fences := extractFencedBlocks(body)
	payloadBlocks := 0
	for _, f := range fences {
		if !strings.Contains(f, "finalize_ticket") && !strings.Contains(f, "ticket_id") {
			continue
		}
		payloadBlocks++
		if !placeholderRe.MatchString(f) {
			t.Errorf("%s: payload example must use angle-bracket placeholders (e.g. <ticket_id>); got:\n%s",
				role, f)
		}
		if uuidShapeRe.MatchString(f) {
			t.Errorf("%s: payload example must NOT contain realistic-shaped UUIDs; got:\n%s",
				role, f)
		}
	}
	if payloadBlocks != 1 {
		t.Errorf("%s: want exactly one fenced code block containing a finalize_ticket payload; found %d",
			role, payloadBlocks)
	}

	// Palace calibration bullets — role-specific per Clarification Q2.
	midTurn := sectionBody(body, "## Mid-turn MemPalace usage (optional)")
	switch role {
	case "engineer":
		for _, marker := range []string{"Skip palace search if", "Search palace if", "In doubt, skip"} {
			if !strings.Contains(midTurn, marker) {
				t.Errorf("%s: ## Mid-turn MemPalace usage (optional) missing engineer calibration marker %q",
					role, marker)
			}
		}
	case "qa-engineer":
		for _, marker := range []string{
			"Always read engineer's wing diary",
			"Skip searches outside `wing_frontend_engineer`",
			"Budget up to 3 calls",
		} {
			if !strings.Contains(midTurn, marker) {
				t.Errorf("%s: ## Mid-turn MemPalace usage (optional) missing qa-engineer calibration marker %q",
					role, marker)
			}
		}
	}

	// Retry framing in ## Failure modes — must name the hint field and
	// frame retry-with-corrections as expected. Whitespace-normalise
	// before substring match so line-wrapped prose still counts.
	failure := sectionBody(body, "## Failure modes")
	failureFlat := collapseWhitespace(failure)
	if !strings.Contains(failureFlat, "hint") {
		t.Errorf("%s: ## Failure modes must name the `hint` field; got:\n%s", role, failure)
	}
	if !strings.Contains(failureFlat, "retrying with corrections is expected") {
		t.Errorf("%s: ## Failure modes must contain 'retrying with corrections is expected'; got:\n%s",
			role, failure)
	}
}

// collapseWhitespace converts every run of whitespace (spaces,
// tabs, newlines) to a single space so substring checks survive
// natural prose wrapping. Markdown agent.md files hard-wrap at
// ~70 cols, so multi-word phrases routinely span two lines.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastSpace = false
	}
	return b.String()
}

// sectionBody returns the text between `heading` and the next `## `
// heading, or to end-of-file if `heading` is the last section. Used
// by requireFinalizeStructure to scope assertions to one section at
// a time.
func sectionBody(body, heading string) string {
	start := strings.Index(body, heading)
	if start < 0 {
		return ""
	}
	rest := body[start+len(heading):]
	end := strings.Index(rest, "\n## ")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// extractFencedBlocks returns the contents (without the fence lines)
// of every ```-delimited block in body. Triple-backtick is the
// canonical JSON-example fence in Garrison's agent.md convention.
func extractFencedBlocks(body string) []string {
	var out []string
	lines := strings.Split(body, "\n")
	inFence := false
	var cur []string
	for _, ln := range lines {
		if strings.HasPrefix(ln, "```") {
			if inFence {
				out = append(out, strings.Join(cur, "\n"))
				cur = cur[:0]
				inFence = false
			} else {
				inFence = true
			}
			continue
		}
		if inFence {
			cur = append(cur, ln)
		}
	}
	return out
}
