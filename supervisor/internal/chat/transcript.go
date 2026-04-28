package chat

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

// AssembleTranscript composes the NDJSON bytes the supervisor pipes
// into the chat container's stdin. Each line is a single JSON object
// in the wire shape claude expects under
//
//	`--input-format stream-json --output-format stream-json`:
//
//	{"type":"user","message":{"role":"user","content":"..."}}
//	{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"..."}]}}
//	{"type":"user","message":{"role":"user","content":"<current turn>"}}
//
// Operator rows always replay; assistant rows replay only when their
// status is 'completed' (clarify Q4: failed / aborted turns are
// excluded so the model doesn't see a half-finished prior assistant
// turn). The current-turn operator content goes last as the final
// user line so claude responds to it.
//
// Output ends with a trailing newline so claude sees a clean EOF
// after the last NDJSON envelope. The caller closes stdin after the
// write to signal end-of-input.
func AssembleTranscript(prior []store.GetSessionTranscriptRow, currentOperatorContent string) ([]byte, error) {
	var buf bytes.Buffer

	for _, r := range prior {
		switch r.Role {
		case "operator":
			line, err := operatorLine(textOf(r.Content))
			if err != nil {
				return nil, err
			}
			buf.Write(line)
		case "assistant":
			if r.Status != "completed" {
				continue
			}
			line, err := assistantLine(textOf(r.Content))
			if err != nil {
				return nil, err
			}
			buf.Write(line)
		default:
			return nil, fmt.Errorf("chat: AssembleTranscript: unknown role %q", r.Role)
		}
	}

	// Current operator turn always lands last, regardless of whether
	// the prior list ends with an operator or assistant entry.
	cur, err := operatorLine(currentOperatorContent)
	if err != nil {
		return nil, err
	}
	buf.Write(cur)

	return buf.Bytes(), nil
}

func operatorLine(content string) ([]byte, error) {
	type wire struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	var w wire
	w.Type = "user"
	w.Message.Role = "user"
	w.Message.Content = content
	b, err := json.Marshal(&w)
	if err != nil {
		return nil, fmt.Errorf("chat: operator line: %w", err)
	}
	return append(b, '\n'), nil
}

func assistantLine(content string) ([]byte, error) {
	// Assistant content uses the structured form claude emits
	// (content blocks with type="text"). Re-emitting the same shape
	// on input ensures claude treats the replay as identical to its
	// own prior output.
	type contentBlock struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type wire struct {
		Type    string `json:"type"`
		Message struct {
			Role    string         `json:"role"`
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	var w wire
	w.Type = "assistant"
	w.Message.Role = "assistant"
	w.Message.Content = []contentBlock{{Type: "text", Text: content}}
	b, err := json.Marshal(&w)
	if err != nil {
		return nil, fmt.Errorf("chat: assistant line: %w", err)
	}
	return append(b, '\n'), nil
}

// textOf safely extracts the string from a *string content column.
// chat_messages.content is NULL during pending/streaming and non-NULL
// at terminal commit; the transcript builder only sees terminal-state
// rows, but the helper guards against NULLs as a defensive measure.
func textOf(content *string) string {
	if content == nil {
		return ""
	}
	return *content
}
