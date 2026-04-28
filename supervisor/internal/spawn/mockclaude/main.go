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
	"encoding/json"
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

const (
	ticketIDToken = "{{TICKET_ID}}"
	toolUseIDFmt  = "toolu_%d"
)

func main() {
	// Catch SIGTERM on a dedicated goroutine so tests that need to
	// verify signal reception observe a marker-file write before the
	// process exits. Handling SIGTERM in the main loop would miss the
	// signal during a #sleep directive because time.Sleep is not
	// interruptible by signal.Notify delivery.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)
	go func() {
		s := <-sigCh
		if marker := os.Getenv("GARRISON_MOCK_CLAUDE_SIGNAL_MARKER"); marker != "" {
			_ = os.WriteFile(marker, []byte(s.String()), 0o600)
		}
		fmt.Fprintf(os.Stderr, "mockclaude: received %s; exiting\n", s)
		os.Exit(143)
	}()

	// M5.1: chat mode is detected from raw os.Args BEFORE flag.Parse
	// because the chat docker-run argv passes `-p` without a value
	// (real claude accepts that shape in stream-json input mode; Go's
	// flag.Parse would consume the next arg as -p's value, eating
	// --input-format and breaking the chat-mode check). Scanning argv
	// directly for the input-format pair sidesteps the issue.
	if hasChatModeArgv(os.Args[1:]) {
		runChatMode()
		return
	}

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

	// M2.2: per-role script dispatch. The supervisor's task description
	// now embeds the role slug ("You are the engineer on ticket..." /
	// "You are the qa-engineer on ticket..."). Mockclaude picks the
	// role-specific script if GARRISON_MOCK_CLAUDE_SCRIPT_{ROLE} is set,
	// otherwise falls back to GARRISON_MOCK_CLAUDE_SCRIPT for M2.1
	// compatibility.
	scriptPath := roleScoped(*taskDesc, "engineer", "GARRISON_MOCK_CLAUDE_SCRIPT_ENGINEER")
	if scriptPath == "" {
		scriptPath = roleScoped(*taskDesc, "qa-engineer", "GARRISON_MOCK_CLAUDE_SCRIPT_QA_ENGINEER")
	}
	if scriptPath == "" {
		scriptPath = os.Getenv("GARRISON_MOCK_CLAUDE_SCRIPT")
	}
	if scriptPath == "" {
		fmt.Fprintln(os.Stderr, "mockclaude: GARRISON_MOCK_CLAUDE_SCRIPT (or a per-role variant) is required")
		os.Exit(2)
	}
	f, err := os.Open(scriptPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mockclaude: open %s: %v\n", scriptPath, err)
		os.Exit(2)
	}
	defer f.Close()

	exitCode := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") {
			// Expand {{TICKET_ID}} in directive arguments so fixture files can
			// reference the ticket ID in file paths and other directive params.
			line = strings.ReplaceAll(line, ticketIDToken, ticketID)
			if err := runDirective(line, ticketID, &exitCode); err != nil {
				fmt.Fprintf(os.Stderr, "mockclaude: directive %q: %v\n", line, err)
				os.Exit(1)
			}
			continue
		}
		if line == "" {
			continue
		}
		line = strings.ReplaceAll(line, ticketIDToken, ticketID)
		fmt.Println(line)
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
		contents = strings.ReplaceAll(contents, ticketIDToken, ticketID)
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

	case "#write-env-to-file":
		// M2.3: write the value of an environment variable to a file in cwd.
		// Format: #write-env-to-file VARNAME FILENAME
		if len(fields) < 3 {
			return fmt.Errorf("#write-env-to-file requires VARNAME FILENAME")
		}
		value := os.Getenv(fields[1])
		return os.WriteFile(fields[2], []byte(value), 0o644)

	case "#dump-env-to-file":
		// M2.3 T013: dump all environment variable names (KEY=VALUE) to a
		// file in cwd. Used by Rule 2 tests to verify no vault-related env
		// var was injected into the subprocess environment.
		// Format: #dump-env-to-file FILENAME
		if len(fields) < 2 {
			return fmt.Errorf("#dump-env-to-file requires FILENAME")
		}
		return os.WriteFile(fields[1], []byte(strings.Join(os.Environ(), "\n")), 0o644)
	case "#":
		// Pure comment.
		return nil

	// M2.2 additions (T016). These directives emit synthetic NDJSON
	// events in the shape the supervisor's pipeline + hygiene logging
	// expect, so integration tests can exercise the mempalace tool-use
	// path, the init-event failure path, and the budget-exceeded path
	// without requiring a real mempalace sidecar.

	case "#init-mcp-servers":
		// Emit a system/init event whose mcp_servers[] array is the
		// JSON payload following the directive. Used by T017/T019 to
		// simulate both-servers-connected and mempalace-failed paths.
		// Format: #init-mcp-servers [{"name":"postgres","status":"connected"},...]
		payload := strings.TrimSpace(strings.TrimPrefix(line, "#init-mcp-servers"))
		if payload == "" {
			return fmt.Errorf("#init-mcp-servers requires a JSON array payload")
		}
		line := `{"type":"system","subtype":"init","cwd":"/workspaces/engineering","session_id":"mock-session-m22","model":"claude-haiku-4-5-20251001","tools":["Read","Write","Bash"],"mcp_servers":` + payload + "}"
		fmt.Println(line)
		return nil

	case "#mempalace-tool-use":
		// Emit a paired assistant/user event: assistant with a tool_use
		// block, followed by a user with a matching tool_result is_error=
		// false. T017/T018 use this to exercise FR-218 logging + hygiene
		// evaluation against a mock-populated palace.
		// Format: #mempalace-tool-use <tool_name> <input-json>
		parts := strings.SplitN(strings.TrimPrefix(line, "#mempalace-tool-use"), " ", 3)
		if len(parts) < 3 {
			return fmt.Errorf("#mempalace-tool-use needs <tool_name> <input-json>")
		}
		toolName := parts[1]
		inputJSON := parts[2]
		toolUseID := fmt.Sprintf(toolUseIDFmt, time.Now().UnixNano()%1_000_000)
		assistant := fmt.Sprintf(
			`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"tool_use","id":"%s","name":"%s","input":%s}]}}`,
			toolUseID, toolName, inputJSON,
		)
		user := fmt.Sprintf(
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"%s","is_error":false,"content":[{"type":"text","text":"ok"}]}]}}`,
			toolUseID,
		)
		fmt.Println(assistant)
		fmt.Println(user)
		return nil

	case "#mempalace-tool-use-error":
		// Same shape as #mempalace-tool-use but emits is_error=true on
		// the tool_result. Error detail comes from the remaining args.
		parts := strings.SplitN(strings.TrimPrefix(line, "#mempalace-tool-use-error"), " ", 3)
		if len(parts) < 3 {
			return fmt.Errorf("#mempalace-tool-use-error needs <tool_name> <detail>")
		}
		toolName := parts[1]
		detail := parts[2]
		toolUseID := fmt.Sprintf(toolUseIDFmt, time.Now().UnixNano()%1_000_000)
		assistant := fmt.Sprintf(
			`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"tool_use","id":"%s","name":"%s","input":{}}]}}`,
			toolUseID, toolName,
		)
		user := fmt.Sprintf(
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"%s","is_error":true,"content":[{"type":"text","text":"%s"}]}]}}`,
			toolUseID, detail,
		)
		fmt.Println(assistant)
		fmt.Println(user)
		return nil

	case "#finalize-tool-use-ok":
		// M2.2.1 T012: emit a successful finalize_ticket tool_use +
		// matching tool_result whose Detail is {"ok":true,"attempt":N}.
		// Supervisor's pipeline observer treats this as a commit trigger
		// and fires OnCommit (T007's WriteFinalize). Payload JSON is
		// passed through verbatim as the tool_use input so the atomic
		// writer receives a real FinalizePayload. {{TICKET_ID}} is
		// substituted in the input JSON.
		//
		// Format: #finalize-tool-use-ok <attempt> <input-json>
		parts := strings.SplitN(strings.TrimPrefix(line, "#finalize-tool-use-ok"), " ", 3)
		if len(parts) < 3 {
			return fmt.Errorf("#finalize-tool-use-ok needs <attempt> <input-json>")
		}
		attempt := parts[1]
		inputJSON := strings.ReplaceAll(parts[2], ticketIDToken, ticketID)
		toolUseID := fmt.Sprintf(toolUseIDFmt, time.Now().UnixNano()%1_000_000)
		assistant := fmt.Sprintf(
			`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"tool_use","id":"%s","name":"finalize_ticket","input":%s}]}}`,
			toolUseID, inputJSON,
		)
		// Detail is the stringified body — the pipeline parses it as JSON.
		detail := fmt.Sprintf(`{\"ok\":true,\"attempt\":%s}`, attempt)
		user := fmt.Sprintf(
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"%s","is_error":false,"content":[{"type":"text","text":"%s"}]}]}}`,
			toolUseID, detail,
		)
		fmt.Println(assistant)
		fmt.Println(user)
		return nil

	case "#finalize-tool-use-fail":
		// M2.2.1 T012: emit a failed finalize_ticket tool_use +
		// matching tool_result whose Detail is {"ok":false,"error_type":...}.
		// Used by retry / exhaustion / never-committed fixtures.
		//
		// Format: #finalize-tool-use-fail <attempt> <error_type> <field>
		parts := strings.SplitN(strings.TrimPrefix(line, "#finalize-tool-use-fail"), " ", 4)
		if len(parts) < 4 {
			return fmt.Errorf("#finalize-tool-use-fail needs <attempt> <error_type> <field>")
		}
		attempt := parts[1]
		errorType := parts[2]
		field := parts[3]
		toolUseID := fmt.Sprintf(toolUseIDFmt, time.Now().UnixNano()%1_000_000)
		assistant := fmt.Sprintf(
			`{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","content":[{"type":"tool_use","id":"%s","name":"finalize_ticket","input":{"ticket_id":"bad"}}]}}`,
			toolUseID,
		)
		detail := fmt.Sprintf(
			`{\"ok\":false,\"attempt\":%s,\"error_type\":\"%s\",\"field\":\"%s\",\"message\":\"mock error\"}`,
			attempt, errorType, field,
		)
		user := fmt.Sprintf(
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"%s","is_error":false,"content":[{"type":"text","text":"%s"}]}]}}`,
			toolUseID, detail,
		)
		fmt.Println(assistant)
		fmt.Println(user)
		return nil

	case "#budget-exceeded":
		// Emit a terminal result event with terminal_reason="budget_
		// exceeded". is_error=true so the ClaudeError path doesn't
		// outrank it; the Adjudicate helper's case for budget-keyword-
		// match should route to ExitBudgetExceeded. total_cost_usd is
		// populated so cost-capture paths exercise correctly.
		line := `{"type":"result","subtype":"error","is_error":false,"duration_ms":1200,"duration_api_ms":800,"total_cost_usd":0.11,"stop_reason":"max_budget","terminal_reason":"budget_exceeded","result":"Maximum budget exceeded; aborted","session_id":"mock-session-m22","permission_denials":[]}`
		fmt.Println(line)
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
// in the sentence "You are the <role> on ticket <UUID>." — the
// pattern is specific enough that a bare regex suffices.
func extractTicketID(taskDescription string) string {
	return ticketIDPattern.FindString(taskDescription)
}

// roleScoped returns the value of envVar iff the task description
// contains the phrase "the <role>" (e.g. "the engineer", "the qa-engineer").
// Returns "" when the role doesn't match or the env var is unset, which
// signals the caller to try the next role or fall back to the global
// GARRISON_MOCK_CLAUDE_SCRIPT.
func roleScoped(taskDesc, role, envVar string) string {
	needle := "the " + role + " "
	if !strings.Contains(taskDesc, needle) {
		return ""
	}
	return os.Getenv(envVar)
}

// runChatMode is the M5.1 chat-mode entrypoint. Reads NDJSON from
// stdin to count prior turns + extract the last user message, then
// emits a canned NDJSON stream matching the spike §8.1 wire shape:
// system/init → message_start (with cache_read populated on turn ≥ 2)
// → content_block_delta×N → assistant → result.
func runChatMode() {
	turnCount, lastUser := readChatTranscript(os.Stdin)
	response := pickChatResponse(lastUser)

	// The init event tools+mcp_servers should reflect what the chat
	// container saw on --strict-mcp-config + --tools "" + --mcp-config.
	// Hard-coded for the mock — production validates via init handler
	// that mcp_servers status="connected".
	initLine := `{"type":"system","subtype":"init","cwd":"/","session_id":"mock-chat-session","model":"claude-sonnet-4-6","tools":[],"mcp_servers":[{"name":"postgres","status":"connected"},{"name":"mempalace","status":"connected"}],"apiKeySource":"none","claude_code_version":"2.1.86"}`

	// cache_creation on turn 1 = N input tokens; cache_read = 0.
	// On turn ≥ 2, cache_read = some non-zero value (proves the
	// supervisor replayed the prefix on stdin per SC-002).
	cacheRead := 0
	cacheCreate := 1500
	if turnCount >= 1 {
		// turnCount is the number of user/assistant PAIRS observed;
		// the current user turn isn't counted as a pair until the
		// assistant replies. So turnCount >= 1 means we saw at least
		// one prior pair → cache hit territory.
		cacheRead = 12000
		cacheCreate = 200
	}
	msgStart := fmt.Sprintf(
		`{"type":"stream_event","session_id":"mock-chat-session","event":{"type":"message_start","message":{"id":"msg-mock","model":"claude-sonnet-4-6","role":"assistant","content":[],"usage":{"input_tokens":3,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":1}}}}`,
		cacheCreate, cacheRead,
	)

	// Split the response into 2-3 deltas so the SSE bridge sees
	// multiple content_block_delta events.
	deltas := splitIntoDeltas(response)
	deltaLines := make([]string, 0, len(deltas))
	for _, d := range deltas {
		dl, err := json.Marshal(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mockclaude: marshal delta: %v\n", err)
			os.Exit(1)
		}
		deltaLines = append(deltaLines,
			fmt.Sprintf(`{"type":"stream_event","session_id":"mock-chat-session","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}}`, dl),
		)
	}

	asstContent, _ := json.Marshal(response)
	asstLine := fmt.Sprintf(
		`{"type":"assistant","message":{"model":"claude-sonnet-4-6","content":[{"type":"text","text":%s}]}}`,
		asstContent,
	)
	resultLine := `{"type":"result","subtype":"success","is_error":false,"duration_ms":150,"total_cost_usd":0.0042,"stop_reason":"end_turn","session_id":"mock-chat-session"}`

	for _, ln := range append(append(append([]string{initLine, msgStart}, deltaLines...), asstLine), resultLine) {
		fmt.Println(ln)
	}
}

// readChatTranscript scans NDJSON from stdin, counting prior turns
// and capturing the last user message content. The supervisor pipes
// the assembled transcript via stdin (per the chat.AssembleTranscript
// contract); claude reads to EOF when stdin closes.
//
// Returns:
//   turnCount: number of complete user/assistant PAIRS observed
//              before the final user turn (so 0 = first user turn,
//              1 = there was at least one prior assistant response)
//   lastUser:  content string of the trailing user message
func readChatTranscript(r io.Reader) (turnCount int, lastUser string) {
	type wire struct {
		Type    string `json:"type"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	users, asst := 0, 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var w wire
		if err := json.Unmarshal(line, &w); err != nil {
			continue
		}
		switch w.Type {
		case "user":
			users++
			lastUser = extractUserContent(w.Message.Content)
		case "assistant":
			asst++
		}
	}
	// turnCount = full pairs = min(users-1, asst). The final user is
	// the one being responded to; all prior users had a paired
	// assistant.
	if users == 0 {
		return 0, ""
	}
	pairs := users - 1
	if asst < pairs {
		pairs = asst
	}
	if pairs < 0 {
		pairs = 0
	}
	return pairs, lastUser
}

func extractUserContent(raw json.RawMessage) string {
	// Two shapes accepted: bare string OR array of {type,text} blocks.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" {
				return b.Text
			}
		}
	}
	return ""
}

// pickChatResponse selects a canned response based on substrings in
// the last user message + (transitively, via stdin scan) the prior
// transcript. Fixtures match the spike §8.1 multi-turn assertion.
func pickChatResponse(lastUser string) string {
	lower := strings.ToLower(lastUser)
	switch {
	case strings.Contains(lower, "favorite color"):
		return "Purple."
	case strings.Contains(lower, "ping"):
		return "pong"
	case strings.Contains(lower, "hello"):
		return "Hi there."
	default:
		return "(canned response)"
	}
}

// splitIntoDeltas chops the response into 1-3 chunks so the chat
// container emits multiple content_block_delta events (matching the
// real-claude streaming pattern). One chunk for short responses;
// two for medium; three for long.
func splitIntoDeltas(s string) []string {
	if len(s) <= 4 {
		return []string{s}
	}
	if len(s) <= 12 {
		mid := len(s) / 2
		return []string{s[:mid], s[mid:]}
	}
	a, b := len(s)/3, 2*len(s)/3
	return []string{s[:a], s[a:b], s[b:]}
}

// hasChatModeArgv scans raw argv for the supervisor's M5.1 chat-mode
// signal: an --input-format=stream-json or --input-format stream-json
// pair. Done before flag.Parse because the chat path's claude argv
// passes `-p` without a value (Go's flag parser would otherwise
// consume the following flag as -p's value).
func hasChatModeArgv(args []string) bool {
	for i, a := range args {
		if a == "--input-format=stream-json" {
			return true
		}
		if a == "--input-format" && i+1 < len(args) && args[i+1] == "stream-json" {
			return true
		}
	}
	return false
}
