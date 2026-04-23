package finalize

import (
	"strings"
	"testing"
)

// TestDecodePositionAtOffsetZero — per T001 completion condition (a):
// empty payload OR offset <= 0 returns (1, 1, ""). Negative offsets
// and zero-offsets on non-empty payloads take the same early-return
// path for consistency with "we haven't read any bytes yet."
func TestDecodePositionAtOffsetZero(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		offset  int64
	}{
		{"empty payload, offset 0", []byte{}, 0},
		{"non-empty payload, offset 0", []byte("{}"), 0},
		{"non-empty payload, negative offset", []byte("abc"), -5},
		{"empty payload, positive offset", []byte{}, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line, col, excerpt := decodePosition(tc.payload, tc.offset)
			if line != 1 || col != 1 || excerpt != "" {
				t.Errorf("got (%d, %d, %q); want (1, 1, \"\")", line, col, excerpt)
			}
		})
	}
}

// TestDecodePositionMidStream — per T001 completion condition (c):
// offset in the middle of a single-line payload returns line=1 and
// the 1-based column; excerpt spans ±20 bytes around the offset.
func TestDecodePositionMidStream(t *testing.T) {
	payload := []byte(`{"ticket_id":"11111111-2222-4333-8444-555566667777","outcome":"x"}`)
	// Position 55 is the 't' in "outcome" (byte layout:
	// 53='o' 54='u' 55='t' 56='c' 57='o' 58='m' 59='e').
	const offset int64 = 55

	line, col, excerpt := decodePosition(payload, offset)

	if line != 1 {
		t.Errorf("line = %d; want 1 (single-line payload)", line)
	}
	if col != 56 {
		t.Errorf("column = %d; want 56 (1-based; offset 55 → col 56)", col)
	}
	if excerpt == "" {
		t.Fatal("excerpt is empty; want non-empty")
	}
	if !strings.Contains(excerpt, "outcome") {
		t.Errorf("excerpt %q should contain 'outcome' (offset 55 is inside it)", excerpt)
	}
	// Excerpt source window is 40 bytes (±20 from offset). With no
	// control-char substitution this payload's excerpt is exactly
	// 40 bytes. Assert at most 40 bytes of payload were sampled by
	// checking the string length (control chars would inflate, but
	// this payload has none).
	if len(excerpt) > 40 {
		t.Errorf("excerpt length = %d; want <= 40 for ASCII-only window", len(excerpt))
	}
}

// TestDecodePositionPastEOF — per T001 completion condition (b):
// offset beyond len(payload) returns line/column at the EOF position
// and an excerpt of the last 40 bytes (or the whole payload if
// shorter than 40 bytes).
func TestDecodePositionPastEOF(t *testing.T) {
	t.Run("short payload", func(t *testing.T) {
		// Truncated JSON — 13 bytes, no newlines, no closing brace.
		payload := []byte(`{"ticket_id":`)
		line, col, excerpt := decodePosition(payload, 100)

		if line != 1 {
			t.Errorf("line = %d; want 1", line)
		}
		if col != 14 {
			t.Errorf("column = %d; want 14 (1-based column after 13 bytes)", col)
		}
		if excerpt != `{"ticket_id":` {
			t.Errorf("excerpt = %q; want the whole short payload", excerpt)
		}
	})

	t.Run("long payload", func(t *testing.T) {
		payload := []byte(strings.Repeat("a", 100))
		line, col, excerpt := decodePosition(payload, 200)

		if line != 1 {
			t.Errorf("line = %d; want 1", line)
		}
		if col != 101 {
			t.Errorf("column = %d; want 101 (1-based column after 100 bytes)", col)
		}
		if len(excerpt) != 40 {
			t.Errorf("excerpt length = %d; want 40 (last 40 bytes of long payload)", len(excerpt))
		}
		if excerpt != strings.Repeat("a", 40) {
			t.Errorf("excerpt = %q; want 40 'a's", excerpt)
		}
	})
}

// TestDecodePositionMultilineNewlines — per T001 completion condition
// (c) + (d): offset on the 3rd line of a 5-line payload returns
// line=3 and the column relative to the start of line 3; raw
// newlines in the excerpt window are sanitised to "·".
func TestDecodePositionMultilineNewlines(t *testing.T) {
	// Byte layout:
	//   line-1\n = 0..6   ('\n' at 6)
	//   line-2\n = 7..13  ('\n' at 13)
	//   line-3\n = 14..20 ('\n' at 20, 'l'=14, 'i'=15, 'n'=16, 'e'=17)
	payload := []byte("line-1\nline-2\nline-3\nline-4\nline-5")
	const offset int64 = 17 // 'e' in "line-3" on line 3

	line, col, excerpt := decodePosition(payload, offset)

	if line != 3 {
		t.Errorf("line = %d; want 3 (offset 17 is on the 3rd line)", line)
	}
	// "line-3": l=col 1, i=col 2, n=col 3, e=col 4. After processing
	// bytes 14..16 (l,i,n) column is 4 when the loop exits at i=17.
	if col != 4 {
		t.Errorf("column = %d; want 4 (1-based column of 'e' on line 3)", col)
	}
	// Excerpt window is ±20 bytes, so it spans into neighbouring
	// lines — those '\n' bytes get replaced with '·'.
	if strings.ContainsRune(excerpt, '\n') {
		t.Errorf("excerpt should not contain raw newlines; got %q", excerpt)
	}
	if !strings.Contains(excerpt, "·") {
		t.Errorf("excerpt should contain '·' (sanitised newline); got %q", excerpt)
	}
}
