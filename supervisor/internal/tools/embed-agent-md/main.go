// embed-agent-md rewrites an agent.md seed inside a goose migration file.
//
// Usage:
//
//	go run ./internal/tools/embed-agent-md [-role <slug>] <agent-md-path> <migration-path>
//
// The tool locates a pair of sentinel comments in the migration file:
//
//	-- +embed-agent-md:<role>:begin
//	...arbitrary content...
//	-- +embed-agent-md:<role>:end
//
// and replaces everything between them with a PostgreSQL dollar-quoted
// string carrying the agent.md file contents verbatim, followed by a
// trailing comma (matching the INSERT statement's value-list shape).
// Re-running the tool is idempotent.
//
// The dollar-quote tag is `<role>_md` (e.g. `engineer_md`, `qa_engineer_md`
// — dashes in the role slug are translated to underscores in the tag).
//
// The tool writes the migration file back atomically via a temp file and
// rename. Errors exit non-zero.
//
// M2.1: hard-coded to `engineer`. M2.2 (T005): parameterized via -role so
// the same binary handles both the engineer UPDATE and the qa-engineer
// INSERT in the M2.2 migration. Default role is "engineer" for backward
// compatibility with the M2.1 Makefile target.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	fs := flag.NewFlagSet("embed-agent-md", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", "engineer", "role slug used in sentinel markers and dollar-quote tag")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: embed-agent-md [-role <slug>] <agent-md-path> <migration-path>")
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if fs.NArg() != 2 {
		fs.Usage()
		os.Exit(2)
	}
	mdPath := fs.Arg(0)
	migPath := fs.Arg(1)

	// Dollar-quote tags can't contain dashes in plain Postgres; translate.
	tag := strings.ReplaceAll(*role, "-", "_") + "_md"
	beginMarker := "-- +embed-agent-md:" + *role + ":begin"
	endMarker := "-- +embed-agent-md:" + *role + ":end"

	mdBytes, err := os.ReadFile(mdPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", mdPath, err)
		os.Exit(1)
	}
	md := string(mdBytes)
	if strings.Contains(md, "$"+tag+"$") {
		fmt.Fprintf(os.Stderr, "refusing to embed: %s contains the dollar-quote tag $%s$ which would terminate the literal\n", mdPath, tag)
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
	if beginLineEnd < len(mig) && mig[beginLineEnd] == '\n' {
		beginLineEnd++
	}

	endLineStart := endIdx
	for endLineStart > 0 && mig[endLineStart-1] != '\n' {
		endLineStart--
	}

	embedded := "  $" + tag + "$" + md + "$" + tag + "$,\n"
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
