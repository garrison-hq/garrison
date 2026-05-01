package garrisonmutate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeDBConn records the SQL+args from each Exec call and lets the test
// inject a return error.
type fakeDBConn struct {
	calls   []fakeDBCall
	execErr error
}

type fakeDBCall struct {
	sql  string
	args []any
}

func (f *fakeDBConn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.calls = append(f.calls, fakeDBCall{sql: sql, args: args})
	return pgconn.CommandTag{}, f.execErr
}

func TestEmitChatMutationNotify_HappyPath(t *testing.T) {
	conn := &fakeDBConn{}
	payload := chatNotifyPayload{
		ChatSessionID:        "session-uuid",
		ChatMessageID:        "message-uuid",
		Verb:                 "create_ticket",
		AffectedResourceID:   "ticket-uuid",
		AffectedResourceType: "ticket",
		Extras:               map[string]string{"k": "v"},
	}
	if err := EmitChatMutationNotify(context.Background(), conn, "ticket.created", payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(conn.calls) != 1 {
		t.Fatalf("expected 1 Exec call, got %d", len(conn.calls))
	}
	c := conn.calls[0]
	if c.sql != "SELECT pg_notify($1, $2)" {
		t.Errorf("SQL = %q; want pg_notify call", c.sql)
	}
	if len(c.args) != 2 {
		t.Fatalf("expected 2 args (channel, body); got %d", len(c.args))
	}
	if c.args[0] != "work.chat.ticket.created" {
		t.Errorf("channel = %v; want work.chat.ticket.created", c.args[0])
	}
	body, ok := c.args[1].(string)
	if !ok {
		t.Fatalf("body arg type = %T; want string", c.args[1])
	}
	for _, want := range []string{"\"verb\":\"create_ticket\"", "\"affectedResourceId\":\"ticket-uuid\"", "\"k\":\"v\""} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q; got %s", want, body)
		}
	}
}

func TestEmitChatMutationNotify_ExecErrorWrapped(t *testing.T) {
	conn := &fakeDBConn{execErr: errors.New("connection refused")}
	err := EmitChatMutationNotify(context.Background(), conn, "ticket.created", chatNotifyPayload{Verb: "x"})
	if err == nil {
		t.Fatal("expected error from Exec failure")
	}
	if !strings.Contains(err.Error(), "pg_notify") || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error %q should wrap pg_notify and underlying cause", err)
	}
}

func TestEmitChatMutationNotify_PayloadTooLargeRejected(t *testing.T) {
	conn := &fakeDBConn{}
	// Build a payload whose Extras content alone exceeds the 8KB pg_notify cap.
	huge := strings.Repeat("a", 9000)
	payload := chatNotifyPayload{
		Verb:   "create_ticket",
		Extras: map[string]string{"oversize": huge},
	}
	err := EmitChatMutationNotify(context.Background(), conn, "ticket.created", payload)
	if err == nil {
		t.Fatal("expected error for oversize payload")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error message should mention size; got %q", err)
	}
	if len(conn.calls) != 0 {
		t.Errorf("Exec should not be called for oversize payload; got %d calls", len(conn.calls))
	}
}
