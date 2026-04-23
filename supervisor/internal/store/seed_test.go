package store_test

import (
	"os"
	"path/filepath"
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

// TestSeedAgentMdStructureAndLength — M2.2.1 T009: both engineer.md
// and qa-engineer.md are (a) between 3000 and 4000 bytes per SC-260;
// (b) contain each required section heading exactly once; (c) mention
// finalize_ticket in the Completion section; (d) do NOT mention
// mempalace_add_drawer or mempalace_kg_add as part of the completion
// protocol (mid-turn section uses is OK).
func TestSeedAgentMdStructureAndLength(t *testing.T) {
	for _, name := range []string{"engineer.md", "qa-engineer.md"} {
		t.Run(name, func(t *testing.T) {
			path := repoSeedPath(t, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			body := string(raw)

			// (a) length bounds.
			if n := len(body); n < 3000 || n > 4000 {
				t.Errorf("%s size = %d bytes; want in [3000, 4000] per SC-260", name, n)
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
		})
	}
}
