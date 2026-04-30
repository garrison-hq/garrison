package garrisonmutate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeAuditTx records inserted audit params and lets tests inject a
// return error.
type fakeAuditTx struct {
	inserted []store.InsertChatMutationAuditParams
	err      error
}

func (f *fakeAuditTx) InsertChatMutationAudit(ctx context.Context, p store.InsertChatMutationAuditParams) (store.InsertChatMutationAuditRow, error) {
	f.inserted = append(f.inserted, p)
	if f.err != nil {
		return store.InsertChatMutationAuditRow{}, f.err
	}
	return store.InsertChatMutationAuditRow{ID: pgtype.UUID{Valid: true, Bytes: [16]byte{0xa, 0xb}}}, nil
}

func TestWriteAudit_MarshalsArgsAndForwards(t *testing.T) {
	tx := &fakeAuditTx{}
	rt := "ticket"
	rid := "tid"
	id, err := WriteAudit(context.Background(), tx, AuditWriteParams{
		ChatSessionID:        pgtype.UUID{Valid: true, Bytes: [16]byte{1}},
		ChatMessageID:        pgtype.UUID{Valid: true, Bytes: [16]byte{2}},
		Verb:                 "create_ticket",
		Args:                 map[string]any{"k": "v"},
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &rid,
		AffectedResourceType: &rt,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !id.Valid {
		t.Error("expected returned id to be valid")
	}
	if len(tx.inserted) != 1 {
		t.Fatalf("expected 1 insert; got %d", len(tx.inserted))
	}
	p := tx.inserted[0]
	if p.Verb != "create_ticket" || p.Outcome != "success" || p.ReversibilityClass != 3 {
		t.Errorf("forwarded params shape wrong: %+v", p)
	}
	if !strings.Contains(string(p.ArgsJsonb), `"k":"v"`) {
		t.Errorf("args not marshalled into ArgsJsonb: %s", p.ArgsJsonb)
	}
}

func TestWriteAudit_UnmarshalableArgsFallbackJSON(t *testing.T) {
	tx := &fakeAuditTx{}
	// channels can't be JSON-marshalled — exercise the marshal-failure
	// fallback path in WriteAudit.
	_, err := WriteAudit(context.Background(), tx, AuditWriteParams{
		Verb: "x",
		Args: map[string]any{"chan": make(chan int)},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(tx.inserted) != 1 {
		t.Fatalf("expected 1 insert despite marshal failure; got %d", len(tx.inserted))
	}
	body := string(tx.inserted[0].ArgsJsonb)
	if !strings.Contains(body, "_audit_marshal_error") {
		t.Errorf("marshal-failure fallback JSON missing sentinel: %s", body)
	}
}

func TestWriteAudit_InsertErrorWrapped(t *testing.T) {
	tx := &fakeAuditTx{err: errors.New("constraint violated")}
	_, err := WriteAudit(context.Background(), tx, AuditWriteParams{Verb: "x"})
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if !strings.Contains(err.Error(), "insert audit row") || !strings.Contains(err.Error(), "constraint violated") {
		t.Errorf("error should wrap context + cause; got %q", err)
	}
}
