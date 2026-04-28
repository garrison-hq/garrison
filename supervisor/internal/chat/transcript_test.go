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

// TestAssembleTranscript_SingleTurn: one operator turn at index 0; the
// assembled NDJSON is exactly one user line.
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
}

// TestAssembleTranscript_MultiTurnReplay: 3 prior turns (operator/
// assistant/operator) + current operator turn → 4 lines in order
// user/assistant/user/user.
func TestAssembleTranscript_MultiTurnReplay(t *testing.T) {
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
	if len(lines) != 4 {
		t.Fatalf("got %d lines; want 4", len(lines))
	}
	wantRoles := []string{"user", "assistant", "user", "user"}
	for i, want := range wantRoles {
		if got := role(t, lines[i]); got != want {
			t.Errorf("line %d role=%q; want %q", i, got, want)
		}
	}
}

// TestAssembleTranscript_SkipsFailedAssistant: an assistant row at
// status='failed' is excluded from replay (clarify Q4).
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
	lines := splitLines(out)
	if len(lines) != 3 {
		t.Fatalf("got %d lines; want 3 (assistant failed skipped)", len(lines))
	}
	for _, ln := range lines {
		if role(t, ln) == "assistant" {
			t.Errorf("unexpected assistant line in replay: %s", ln)
		}
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
	lines := splitLines(out)
	if len(lines) != 2 {
		t.Fatalf("got %d lines; want 2", len(lines))
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
	// Re-decode and verify the content survived intact.
	var w struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if w.Message.Content != tricky {
		t.Errorf("round-trip lost data:\n  got %q\n want %q", w.Message.Content, tricky)
	}
}

func splitLines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
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
