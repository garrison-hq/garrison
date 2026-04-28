package chat

import (
	"strings"
	"testing"
)

// TestChannelNames_MatchSpec pins the literal channel-name strings.
// Adding/renaming a channel requires a code change here AND an update
// to spec FR-050 / FR-051 / FR-070 / FR-071 (compile-time discipline).
func TestChannelNames_MatchSpec(t *testing.T) {
	cases := []struct {
		got, want string
	}{
		{ChannelChatMessageSent, "chat.message.sent"},
		{ChannelChatAssistantDelta, "chat.assistant.delta"},
		{ChannelWorkChatSessionStarted, "work.chat.session_started"},
		{ChannelWorkChatMessageSent, "work.chat.message_sent"},
		{ChannelWorkChatSessionEnded, "work.chat.session_ended"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("channel mismatch: got %q want %q", c.got, c.want)
		}
		if strings.Contains(c.got, " ") {
			t.Errorf("channel %q contains space", c.got)
		}
	}
}

// TestMaxNotifyPayload_StaysUnder8KB verifies the 7KB ceiling stays
// below Postgres's 8000-byte NOTIFY payload limit with safety margin.
func TestMaxNotifyPayload_StaysUnder8KB(t *testing.T) {
	if MaxNotifyPayloadBytes >= 8000 {
		t.Errorf("MaxNotifyPayloadBytes = %d; must be < 8000 (Postgres NOTIFY ceiling)", MaxNotifyPayloadBytes)
	}
	if MaxNotifyPayloadBytes < 1024 {
		t.Errorf("MaxNotifyPayloadBytes = %d; suspiciously low (<1KB)", MaxNotifyPayloadBytes)
	}
}
