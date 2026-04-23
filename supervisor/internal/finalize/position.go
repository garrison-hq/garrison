package finalize

// decodePosition converts a *json.SyntaxError.Offset (a byte index
// into the raw JSON payload) into 1-based line + column coordinates
// and a short excerpt centred on the failing token. Pure function;
// no I/O, no allocations beyond the returned string.
//
// Properties:
//   - Empty payload OR offset <= 0 → (1, 1, "")
//   - offset > len(payload) → line/column at EOF; excerpt is the
//     last 40 bytes of payload (or the whole payload if shorter)
//   - in-bounds offset → line walked from start, column relative to
//     start of current line, excerpt of ±20 bytes around offset
//     (clamped to payload bounds)
//
// The excerpt sanitises ASCII control characters (< 0x20 and 0x7F)
// to the middle-dot "·" so the value is safe to embed in a hint
// string or a log line. Substituting "·" inflates each replaced
// byte to 2 UTF-8 bytes, so the resulting string may exceed the
// 40-byte source window — intentional, per FR-301's "at most 40
// chars" applied to the *source* window rather than the output.
func decodePosition(payload []byte, offset int64) (line, column int, excerpt string) {
	if len(payload) == 0 || offset <= 0 {
		return 1, 1, ""
	}

	walkTo := offset
	if walkTo > int64(len(payload)) {
		walkTo = int64(len(payload))
	}

	line = 1
	column = 1
	for i := int64(0); i < walkTo; i++ {
		if payload[i] == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}

	var start, end int64
	if offset > int64(len(payload)) {
		end = int64(len(payload))
		start = end - 40
		if start < 0 {
			start = 0
		}
	} else {
		start = offset - 20
		if start < 0 {
			start = 0
		}
		end = offset + 20
		if end > int64(len(payload)) {
			end = int64(len(payload))
		}
	}
	excerpt = sanitizeExcerpt(payload[start:end])
	return line, column, excerpt
}

// sanitizeExcerpt replaces ASCII control characters (bytes < 0x20
// and the DEL byte 0x7F) with the middle-dot "·" (U+00B7, UTF-8
// bytes 0xC2 0xB7). Keeping the excerpt free of raw newlines and
// tabs makes it safe to drop into a slog line or an agent-facing
// hint without breaking log viewers or confusing downstream parsers.
func sanitizeExcerpt(b []byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c < 0x20 || c == 0x7F {
			out = append(out, 0xC2, 0xB7)
		} else {
			out = append(out, c)
		}
	}
	return string(out)
}
