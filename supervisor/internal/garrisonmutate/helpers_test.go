package garrisonmutate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestStringPtrOrNil(t *testing.T) {
	if got := stringPtrOrNil(""); got != nil {
		t.Errorf("empty string should return nil, got %q", *got)
	}
	if got := stringPtrOrNil("x"); got == nil || *got != "x" {
		t.Errorf("non-empty string should round-trip, got %v", got)
	}
}

func TestDerefOrNil(t *testing.T) {
	if got := derefOrNil(nil); got != nil {
		t.Errorf("nil pointer should yield nil, got %v", got)
	}
	s := "hello"
	if got := derefOrNil(&s); got != "hello" {
		t.Errorf("non-nil pointer should yield value, got %v", got)
	}
}

func TestUuidString(t *testing.T) {
	if got := uuidString(pgtype.UUID{Valid: false}); got != "" {
		t.Errorf("invalid UUID should yield empty string, got %q", got)
	}
	u := pgtype.UUID{Valid: true, Bytes: [16]byte{0xa, 0xb, 0xc, 0xd, 0xe, 0xf, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}}
	got := uuidString(u)
	if len(got) != 36 || strings.Count(got, "-") != 4 {
		t.Errorf("uuidString returned malformed UUID: %q", got)
	}
}

func TestIsLockNotAvailable(t *testing.T) {
	if isLockNotAvailable(nil) {
		t.Error("nil should not be classified as lock-not-available")
	}
	if isLockNotAvailable(errors.New("bare")) {
		t.Error("non-pgx error should not be classified")
	}
	notTheCode := &pgconn.PgError{Code: "42P01"}
	if isLockNotAvailable(notTheCode) {
		t.Error("unrelated pg error should not be classified")
	}
	lockNA := &pgconn.PgError{Code: "55P03"}
	if !isLockNotAvailable(lockNA) {
		t.Error("55P03 should be classified as lock-not-available")
	}
}

func TestClassifyTicketLockErr(t *testing.T) {
	deps := Deps{
		ChatSessionID: pgtype.UUID{Valid: true, Bytes: [16]byte{1}},
		ChatMessageID: pgtype.UUID{Valid: true, Bytes: [16]byte{2}},
	}

	t.Run("nil err means lock acquired", func(t *testing.T) {
		r, audErr, ok := classifyTicketLockErr(context.Background(), deps, "edit_ticket", nil, 2, "", nil)
		if !ok || audErr != nil || r.Success {
			t.Errorf("nil err should return ok=true with empty result; got ok=%v err=%v r=%+v", ok, audErr, r)
		}
	})

	t.Run("55P03 maps to ticket_state_changed", func(t *testing.T) {
		err := &pgconn.PgError{Code: "55P03"}
		r, _, ok := classifyTicketLockErr(context.Background(), deps, "edit_ticket", nil, 2, "", err)
		if ok {
			t.Error("should classify as not-locked")
		}
		if r.ErrorKind != string(ErrTicketStateChanged) {
			t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrTicketStateChanged)
		}
	})

	t.Run("ErrNoRows maps to resource_not_found", func(t *testing.T) {
		r, _, ok := classifyTicketLockErr(context.Background(), deps, "edit_ticket", nil, 2, "abc", pgx.ErrNoRows)
		if ok {
			t.Error("should classify as not-locked")
		}
		if r.ErrorKind != string(ErrResourceNotFound) {
			t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrResourceNotFound)
		}
	})

	t.Run("unknown err returns wrapped", func(t *testing.T) {
		boom := errors.New("disk full")
		_, audErr, ok := classifyTicketLockErr(context.Background(), deps, "edit_ticket", nil, 2, "abc", boom)
		if ok {
			t.Error("should classify as not-locked")
		}
		if audErr == nil || !strings.Contains(audErr.Error(), "lock") {
			t.Errorf("expected wrapped lock err, got %v", audErr)
		}
	})
}

func TestMergeTicketFields(t *testing.T) {
	ac := "old criteria"
	before := store.Ticket{
		Objective:          "old objective",
		AcceptanceCriteria: &ac,
		Metadata:           []byte(`{"k":"v"}`),
	}

	t.Run("no overrides keeps before values", func(t *testing.T) {
		obj, accept, meta, vRes := mergeTicketFields(before, EditTicketArgs{})
		if vRes != nil {
			t.Fatalf("unexpected validation failure: %+v", vRes)
		}
		if obj != "old objective" {
			t.Errorf("objective changed: %q", obj)
		}
		if accept == nil || *accept != "old criteria" {
			t.Errorf("acceptance changed: %v", accept)
		}
		if string(meta) != `{"k":"v"}` {
			t.Errorf("metadata changed: %s", meta)
		}
	})

	t.Run("overrides applied", func(t *testing.T) {
		newObj := "new"
		newAccept := "new criteria"
		obj, accept, meta, vRes := mergeTicketFields(before, EditTicketArgs{
			Objective:          &newObj,
			AcceptanceCriteria: &newAccept,
			Metadata:           map[string]any{"x": 1},
		})
		if vRes != nil {
			t.Fatalf("unexpected validation failure: %+v", vRes)
		}
		if obj != "new" || *accept != "new criteria" {
			t.Errorf("expected overrides applied; got obj=%q accept=%v", obj, accept)
		}
		if !strings.Contains(string(meta), `"x":1`) {
			t.Errorf("metadata override missing: %s", meta)
		}
	})

	t.Run("metadata marshal failure surfaces validation result", func(t *testing.T) {
		// channels can't be JSON-marshalled
		_, _, _, vRes := mergeTicketFields(before, EditTicketArgs{
			Metadata: map[string]any{"chan": make(chan int)},
		})
		if vRes == nil {
			t.Fatal("expected validation failure")
		}
		if vRes.ErrorKind != string(ErrValidationFailed) {
			t.Errorf("ErrorKind = %q; want %q", vRes.ErrorKind, ErrValidationFailed)
		}
	})
}

func TestMergeAgentConfigFields(t *testing.T) {
	wing := "ops"
	md := "old md"
	before := store.Agent{
		Model:      "claude-old",
		AgentMd:    md,
		PalaceWing: &wing,
	}

	t.Run("no overrides keeps before values", func(t *testing.T) {
		m, am, w := mergeAgentConfigFields(before, EditAgentConfigArgs{})
		if m != "claude-old" || am != "old md" || w == nil || *w != "ops" {
			t.Errorf("merge altered values: m=%q am=%q w=%v", m, am, w)
		}
	})

	t.Run("model override only", func(t *testing.T) {
		newModel := "claude-new"
		m, am, w := mergeAgentConfigFields(before, EditAgentConfigArgs{Model: &newModel})
		if m != "claude-new" || am != "old md" || w == nil || *w != "ops" {
			t.Errorf("partial override broke: m=%q am=%q w=%v", m, am, w)
		}
	})

	t.Run("palace_wing pointer override propagates", func(t *testing.T) {
		newWing := "growth"
		_, _, w := mergeAgentConfigFields(before, EditAgentConfigArgs{PalaceWing: &newWing})
		if w == nil || *w != "growth" {
			t.Errorf("palace_wing override missing: %v", w)
		}
	})
}

func TestScanForSecrets(t *testing.T) {
	cases := []struct {
		name  string
		input string
		hits  bool
	}{
		{"clean", "no secrets here, just prose", false},
		{"sk-prefix", "key: sk-abcdefghij1234567890abcdef", true},
		{"slack bot", "token=xoxb-1234567890abcdefghijklmnop", true},
		{"aws key", "AKIA1234567890ABCDEF", true},
		{"pem header", "-----BEGIN RSA PRIVATE KEY-----", true},
		{"github pat", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", true},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := scanForSecrets(tc.input)
			if tc.hits && len(hits) == 0 {
				t.Errorf("expected at least one hit on %q", tc.input)
			}
			if !tc.hits && len(hits) != 0 {
				t.Errorf("expected zero hits on %q; got %v", tc.input, hits)
			}
		})
	}
}

func TestRunLeakScanGate(t *testing.T) {
	deps := Deps{
		ChatSessionID: pgtype.UUID{Valid: true, Bytes: [16]byte{1}},
		ChatMessageID: pgtype.UUID{Valid: true, Bytes: [16]byte{2}},
	}

	t.Run("nil agent_md skips the gate", func(t *testing.T) {
		_, _, blocked := runLeakScanGate(context.Background(), deps, EditAgentConfigArgs{
			AgentRoleSlug: "engineering.engineer",
		})
		if blocked {
			t.Error("nil AgentMD should not block")
		}
	})

	t.Run("clean agent_md does not block", func(t *testing.T) {
		md := "Read tickets. Run tools. Write to MemPalace."
		_, _, blocked := runLeakScanGate(context.Background(), deps, EditAgentConfigArgs{
			AgentRoleSlug: "engineering.engineer",
			AgentMD:       &md,
		})
		if blocked {
			t.Error("clean MD should not block")
		}
	})

	t.Run("dirty agent_md blocks with leak_scan_failed", func(t *testing.T) {
		md := "use this key: sk-abcdefghij1234567890abcdef"
		r, _, blocked := runLeakScanGate(context.Background(), deps, EditAgentConfigArgs{
			AgentRoleSlug: "engineering.engineer",
			AgentMD:       &md,
		})
		if !blocked {
			t.Fatal("dirty MD should block")
		}
		if r.ErrorKind != string(ErrLeakScanFailed) {
			t.Errorf("ErrorKind = %q; want %q", r.ErrorKind, ErrLeakScanFailed)
		}
	})
}

func TestFailureHelpers(t *testing.T) {
	if r := validationFailure("nope"); r.Success || r.ErrorKind != string(ErrValidationFailed) || r.Message != "nope" {
		t.Errorf("validationFailure shape: %+v", r)
	}
	if r := resourceNotFound("missing %s", "x"); r.ErrorKind != string(ErrResourceNotFound) || !strings.Contains(r.Message, "missing x") {
		t.Errorf("resourceNotFound shape: %+v", r)
	}
	if r := ticketStateChanged("conflict"); r.ErrorKind != string(ErrTicketStateChanged) {
		t.Errorf("ticketStateChanged shape: %+v", r)
	}
	if r := concurrencyCapFull("full"); r.ErrorKind != string(ErrConcurrencyCapFull) {
		t.Errorf("concurrencyCapFull shape: %+v", r)
	}
	if r := leakScanFailure("leak"); r.ErrorKind != string(ErrLeakScanFailed) {
		t.Errorf("leakScanFailure shape: %+v", r)
	}
}
