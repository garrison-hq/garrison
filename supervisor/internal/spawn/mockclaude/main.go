// Command mockclaude is a drop-in replacement for the real `claude`
// binary used by M2.1's integration tests. It drains every flag the
// supervisor passes (so argv matches real-Claude 2.1.117 byte-for-byte),
// extracts the ticket ID from the -p task description, emits a canned
// NDJSON stream from a script file, and optionally performs side effects
// (writing hello.txt with the ticket ID as contents) per directive lines
// in the script.
//
// The script path arrives via env (GARRISON_MOCK_CLAUDE_SCRIPT). Lines
// starting with '#' are directives consumed by the mock; other lines
// are emitted verbatim to stdout after substituting the literal token
// {{TICKET_ID}} with the parsed ticket ID. The substitution lets fixture
// files be parameterized without per-test regeneration.
//
// Directives (all line-wide, case-sensitive):
//   - #write-hello-txt                 — write hello.txt in cwd with
//     the ticket ID as contents.
//   - #write-hello-txt-contents X      — same, but with contents X.
//     X may contain {{TICKET_ID}}.
//   - #sleep 250ms                     — time.Sleep before the next
//     non-directive line.
//   - #exit-code N                     — exit with status N after the
//     stream ends (default 0).
//   - #marker-file <path>              — touch <path> on startup so
//     tests can observe this mock
//     invocation externally.
//
// Unknown directives produce a stderr warning and are otherwise ignored,
// so adding new ones to a script does not crash older mocks.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var ticketIDPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func main() {
	// Catch SIGTERM so tests that need to verify signal reception can
	// observe a marker-file write before the process exits.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	fs := flag.NewFlagSet("mockclaude", flag.ContinueOnError)
	// Silence flag's default complaint-on-unknown — we intentionally
	// declare every flag the supervisor passes so Parse succeeds even
	// when the supervisor adds new ones that don't concern us.
	fs.SetOutput(io.Discard)
	taskDesc := fs.String("p", "", "task description")
	fs.String("output-format", "", "")
	fs.Bool("verbose", false, "")
	fs.Bool("no-session-persistence", false, "")
	fs.String("model", "", "")
	fs.String("max-budget-usd", "", "")
	fs.String("mcp-config", "", "")
	fs.Bool("strict-mcp-config", false, "")
	fs.String("system-prompt", "", "")
	fs.String("permission-mode", "", "")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "mockclaude: flag parse: %v\n", err)
		os.Exit(2)
	}

	ticketID := extractTicketID(*taskDesc)

	scriptPath := os.Getenv("GARRISON_MOCK_CLAUDE_SCRIPT")
	if scriptPath == "" {
		fmt.Fprintln(os.Stderr, "mockclaude: GARRISON_MOCK_CLAUDE_SCRIPT is required")
		os.Exit(2)
	}
	f, err := os.Open(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mockclaude: open %s: %v\n", scriptPath, err)
		os.Exit(2)
	}
	defer f.Close()

	exitCode := 0
	sigReceived := make(chan os.Signal, 1)
	go func() {
		s := <-sigCh
		sigReceived <- s
	}()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			if err := runDirective(line, ticketID, &exitCode); err != nil {
				fmt.Fprintf(os.Stderr, "mockclaude: directive %q: %v\n", line, err)
				os.Exit(1)
			}
			continue
		}
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, "{{TICKET_ID}}", ticketID)
		fmt.Println(line)
		// Bounce out early if SIGTERM arrived mid-script so tests can
		// observe supervisor-initiated termination.
		select {
		case s := <-sigReceived:
			fmt.Fprintf(os.Stderr, "mockclaude: received %s mid-script; exiting\n", s)
			if marker := os.Getenv("GARRISON_MOCK_CLAUDE_SIGNAL_MARKER"); marker != "" {
				_ = os.WriteFile(marker, []byte(s.String()), 0o600)
			}
			os.Exit(143)
		default:
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "mockclaude: scan: %v\n", err)
		os.Exit(1)
	}

	os.Exit(exitCode)
}

// runDirective mutates local state (exit code, sleep) or performs the
// named side effect. ticketID is the UUID extracted from -p, used to
// substitute {{TICKET_ID}} in directive payloads.
func runDirective(line, ticketID string, exitCode *int) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "#write-hello-txt":
		return writeHelloTxt(ticketID)
	case "#write-hello-txt-contents":
		// Everything after the directive name is the literal contents,
		// including any embedded spaces. A single whitespace after the
		// directive separates the two; contents may contain spaces.
		contents := strings.TrimPrefix(line, "#write-hello-txt-contents")
		contents = strings.TrimLeft(contents, " \t")
		contents = strings.ReplaceAll(contents, "{{TICKET_ID}}", ticketID)
		return os.WriteFile("hello.txt", []byte(contents), 0o644)
	case "#sleep":
		if len(fields) < 2 {
			return fmt.Errorf("#sleep requires a duration argument")
		}
		d, err := time.ParseDuration(fields[1])
		if err != nil {
			return fmt.Errorf("parse duration: %w", err)
		}
		time.Sleep(d)
		return nil
	case "#exit-code":
		if len(fields) < 2 {
			return fmt.Errorf("#exit-code requires an integer argument")
		}
		var n int
		if _, err := fmt.Sscanf(fields[1], "%d", &n); err != nil {
			return fmt.Errorf("parse exit code: %w", err)
		}
		*exitCode = n
		return nil
	case "#marker-file":
		if len(fields) < 2 {
			return fmt.Errorf("#marker-file requires a path argument")
		}
		return os.WriteFile(fields[1], []byte(ticketID), 0o600)
	case "#":
		// Pure comment.
		return nil
	default:
		fmt.Fprintf(os.Stderr, "mockclaude: unknown directive %q; ignoring\n", fields[0])
		return nil
	}
}

// writeHelloTxt creates hello.txt in cwd containing exactly the ticket
// ID (no trailing newline) — the acceptance shape the engineer agent is
// contracted to produce on success (plan Appendix A).
func writeHelloTxt(ticketID string) error {
	path := filepath.Join(".", "hello.txt")
	return os.WriteFile(path, []byte(ticketID), 0o644)
}

// extractTicketID pulls the first canonical-form UUID out of the -p
// task description. The supervisor's argv format embeds the ticket ID
// in the sentence "You are the engineer on ticket <UUID>." — the
// pattern is specific enough that a bare regex suffices.
func extractTicketID(taskDescription string) string {
	return ticketIDPattern.FindString(taskDescription)
}
