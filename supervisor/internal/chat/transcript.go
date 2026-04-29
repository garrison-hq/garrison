package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

// AssembleTranscript composes the NDJSON bytes the supervisor pipes
// into the chat container's stdin. The output is exactly ONE
// `{"type":"user","message":{...}}` line whose content is the full
// conversation history followed by the current operator message.
//
// Why one line instead of one-per-turn: real claude in
// `--input-format stream-json --output-format stream-json --print`
// mode treats each input `user` message as a separate turn and emits
// one `result` event per turn (observed empirically — N>0 prior turns
// → 2 result events: one for the first message in the stream, one
// for the last). That broke the dashboard, which committed both
// terminals into the same chat_messages row. Flattening the
// transcript into a single user message gives claude one turn → one
// response, which is what the chat surface expects.
//
// Format:
//
//	## Prior conversation
//
//	**Operator:** ...
//	**You:** ...
//
//	## Current message
//
//	<current operator content>
//
// Operator rows always replay; assistant rows replay only when their
// status is 'completed' (failed/aborted excluded so claude doesn't see
// half-finished prior turns). On the first turn (no prior history) the
// "Prior conversation" + "Current message" headers are omitted — the
// content is just the operator's text verbatim.
//
// Output ends with a trailing newline so claude sees a clean EOF
// after the NDJSON envelope. The caller closes stdin after the write
// to signal end-of-input.
func AssembleTranscript(prior []store.GetSessionTranscriptRow, currentOperatorContent string) ([]byte, error) {
	var historyBuf strings.Builder
	hasPrior := false
	for _, r := range prior {
		switch r.Role {
		case "operator":
			fmt.Fprintf(&historyBuf, "**Operator:** %s\n\n", textOf(r.Content))
			hasPrior = true
		case "assistant":
			if r.Status != "completed" {
				continue
			}
			fmt.Fprintf(&historyBuf, "**You:** %s\n\n", textOf(r.Content))
			hasPrior = true
		default:
			return nil, fmt.Errorf("chat: AssembleTranscript: unknown role %q", r.Role)
		}
	}

	var content strings.Builder
	if hasPrior {
		content.WriteString("## Prior conversation\n\n")
		content.WriteString(historyBuf.String())
		content.WriteString("## Current message\n\n")
	}
	content.WriteString(currentOperatorContent)

	line, err := userLine(content.String())
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Write(line)
	return buf.Bytes(), nil
}

func userLine(content string) ([]byte, error) {
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
		return nil, fmt.Errorf("chat: user line: %w", err)
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
