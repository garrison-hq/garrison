// embed-agent-md rewrites an agent.md seed inside a goose migration file.
//
// Usage:
//
//	go run ./internal/tools/embed-agent-md <agent-md-path> <migration-path>
//
// The tool locates a pair of sentinel comments in the migration file:
//
//	-- +embed-agent-md:engineer:begin
//	...arbitrary content...
//	-- +embed-agent-md:engineer:end
//
// and replaces everything between them with a PostgreSQL dollar-quoted
// string carrying the agent.md file contents verbatim, followed by a
// trailing comma (matching the INSERT statement's value-list shape).
// Re-running the tool is idempotent.
//
// The tool writes the migration file back atomically via a temp file and
// rename. Errors exit non-zero. stdout reports the number of bytes the
// embedded block changed by; stderr carries any diagnostic.
//
// Scope for M2.1: the marker label is hard-coded as "engineer"; the dollar-
// quote tag is "engineer_md". Future milestones that embed other agent.md
// files will either parameterize this tool or ship sibling copies keyed to
// their own label. YAGNI keeps M2.1 minimal.
package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	beginMarker = "-- +embed-agent-md:engineer:begin"
	endMarker   = "-- +embed-agent-md:engineer:end"
	dollarTag   = "engineer_md"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: embed-agent-md <agent-md-path> <migration-path>")
		os.Exit(2)
	}
	mdPath := os.Args[1]
	migPath := os.Args[2]

	mdBytes, err := os.ReadFile(mdPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", mdPath, err)
		os.Exit(1)
	}
	md := string(mdBytes)
	if strings.Contains(md, "$"+dollarTag+"$") {
		fmt.Fprintf(os.Stderr, "refusing to embed: %s contains the dollar-quote tag $%s$ which would terminate the literal\n", mdPath, dollarTag)
		os.Exit(1)
	}

	migBytes, err := os.ReadFile(migPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", migPath, err)
		os.Exit(1)
	}
	mig := string(migBytes)

	beginIdx := strings.Index(mig, beginMarker)
	endIdx := strings.Index(mig, endMarker)
	if beginIdx < 0 || endIdx < 0 {
		fmt.Fprintf(os.Stderr, "migration %s is missing one of the sentinel markers (%q, %q)\n", migPath, beginMarker, endMarker)
		os.Exit(1)
	}
	if endIdx < beginIdx {
		fmt.Fprintln(os.Stderr, "end marker precedes begin marker; refusing to edit")
		os.Exit(1)
	}

	// Replace the span from the end of the begin line to the start of the
	// end-marker line with the freshly-embedded dollar-quoted literal.
	beginLineEnd := beginIdx + len(beginMarker)
	// Skip the newline right after the begin marker if present; leave one
	// newline between the marker and the literal for readability.
	if beginLineEnd < len(mig) && mig[beginLineEnd] == '\n' {
		beginLineEnd++
	}

	// Rewind to the start of the end-marker line (keeping its leading indent
	// intact) so the generated block ends just before that line.
	endLineStart := endIdx
	for endLineStart > 0 && mig[endLineStart-1] != '\n' {
		endLineStart--
	}

	embedded := "  $" + dollarTag + "$" + md + "$" + dollarTag + "$,\n"
	updated := mig[:beginLineEnd] + embedded + mig[endLineStart:]

	tmp := migPath + ".embed-tmp"
	if err := os.WriteFile(tmp, []byte(updated), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write temp %s: %v\n", tmp, err)
		os.Exit(1)
	}
	if err := os.Rename(tmp, migPath); err != nil {
		fmt.Fprintf(os.Stderr, "rename %s -> %s: %v\n", tmp, migPath, err)
		os.Exit(1)
	}

	delta := len(updated) - len(mig)
	fmt.Printf("embed-agent-md: wrote %d bytes (delta %+d) into %s\n", len(embedded), delta, migPath)
}
