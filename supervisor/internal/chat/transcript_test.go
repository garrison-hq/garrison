package chat

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

func ptr(s string) *string { return &s }

// TestAssembleTranscript_SingleTurn: one operator turn at index 0
// emits exactly one user NDJSON line whose content is the operator
// message verbatim — no "Prior conversation" / "Current message"
// headers when there's no history.
func TestAssembleTranscript_SingleTurn(t *testing.T) {
	out, err := AssembleTranscript(nil, "hello")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines; want 1: %q", len(lines), out)
	}
	if got := role(t, lines[0]); got != "user" {
		t.Errorf("line 0 role=%q; want user", got)
	}
	got := contentOf(t, lines[0])
	if got != "hello" {
		t.Errorf("content=%q; want %q", got, "hello")
	}
	if strings.Contains(got, "Prior conversation") || strings.Contains(got, "Current message") {
		t.Errorf("first-turn content should not carry history headers: %q", got)
	}
}

// TestAssembleTranscript_MultiTurn_FlattensIntoOneUserLine: 3 prior
// turns + the current op message produce ONE user NDJSON line whose
// content includes the prior history + current message under
// markdown headers. Real claude in stream-json input mode emits one
// result event per `user` line, so flattening into a single line is
// what makes the chat surface work.
func TestAssembleTranscript_MultiTurn_FlattensIntoOneUserLine(t *testing.T) {
	prior := []store.GetSessionTranscriptRow{
		{Role: "operator", Status: "completed", TurnIndex: 0, Content: ptr("favorite color is purple")},
		{Role: "assistant", Status: "completed", TurnIndex: 1, Content: ptr("Noted.")},
		{Role: "operator", Status: "completed", TurnIndex: 2, Content: ptr("ok")},
	}
	out, err := AssembleTranscript(prior, "what is my favorite color?")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines; want 1 (flattened): %q", len(lines), out)
	}
	if got := role(t, lines[0]); got != "user" {
		t.Errorf("line role=%q; want user", got)
	}
	c := contentOf(t, lines[0])
	for _, want := range []string{
		"## Prior conversation",
		"**Operator:** favorite color is purple",
		"**You:** Noted.",
		"**Operator:** ok",
		"## Current message",
		"what is my favorite color?",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("content missing %q\n--- full content ---\n%s", want, c)
		}
	}
}

// TestAssembleTranscript_SkipsFailedAssistant: an assistant row at
// status='failed' is excluded from the history block (clarify Q4).
func TestAssembleTranscript_SkipsFailedAssistant(t *testing.T) {
	prior := []store.GetSessionTranscriptRow{
		{Role: "operator", Status: "completed", TurnIndex: 0, Content: ptr("q1")},
		{Role: "assistant", Status: "failed", TurnIndex: 1, Content: ptr("DROPPED")},
		{Role: "operator", Status: "completed", TurnIndex: 2, Content: ptr("q2")},
	}
	out, err := AssembleTranscript(prior, "q3")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	c := contentOf(t, splitLines(out)[0])
	if strings.Contains(c, "DROPPED") {
		t.Errorf("failed assistant text leaked into transcript: %s", c)
	}
	if !strings.Contains(c, "**Operator:** q1") || !strings.Contains(c, "**Operator:** q2") {
		t.Errorf("operator turns missing from content: %s", c)
	}
}

// TestAssembleTranscript_SkipsAbortedAssistant: same as failed but
// for status='aborted'.
func TestAssembleTranscript_SkipsAbortedAssistant(t *testing.T) {
	prior := []store.GetSessionTranscriptRow{
		{Role: "operator", Status: "completed", TurnIndex: 0, Content: ptr("q1")},
		{Role: "assistant", Status: "aborted", TurnIndex: 1, Content: ptr("DROPPED")},
	}
	out, err := AssembleTranscript(prior, "q2")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	c := contentOf(t, splitLines(out)[0])
	if strings.Contains(c, "DROPPED") {
		t.Errorf("aborted assistant text leaked into transcript: %s", c)
	}
}

// TestAssembleTranscript_HandlesUnicodeAndJSONEscapes: content with "
// + newline + emoji round-trips correctly through json.Marshal.
func TestAssembleTranscript_HandlesUnicodeAndJSONEscapes(t *testing.T) {
	tricky := "she said \"hi\"\nand 🚀ed away"
	out, err := AssembleTranscript(nil, tricky)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := contentOf(t, splitLines(out)[0]); got != tricky {
		t.Errorf("round-trip lost data:\n  got %q\n want %q", got, tricky)
	}
}

func splitLines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func role(t *testing.T, line string) string {
	t.Helper()
	var w struct {
		Type    string `json:"type"`
		Message struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return w.Message.Role
}

func contentOf(t *testing.T, line string) string {
	t.Helper()
	var w struct {
		Type    string `json:"type"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &w); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return w.Message.Content
}
