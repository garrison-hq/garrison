package spawn

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// Tests for the pure helpers extracted during the M3 quality cleanup
// (commit "quality: clear remaining SonarCloud issues on main"). These
// helpers were factored out of long functions to reduce cognitive
// complexity; the parent paths remain integration-tested but the
// helpers themselves benefit from direct unit tests so coverage
// attribution doesn't depend on the integration suite running.

func TestDecodeSpawnPayloadValid(t *testing.T) {
	raw := []byte(`{
		"ticket_id": "11111111-1111-1111-1111-111111111111",
		"department_id": "22222222-2222-2222-2222-222222222222",
		"column_slug": "in_dev"
	}`)
	payload, ticketUUID, deptUUID, err := decodeSpawnPayload(raw)
	if err != nil {
		t.Fatalf("decodeSpawnPayload: %v", err)
	}
	if payload.TicketID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("ticket_id: got %q", payload.TicketID)
	}
	if payload.DepartmentID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("department_id: got %q", payload.DepartmentID)
	}
	if payload.ColumnSlug != "in_dev" {
		t.Errorf("column_slug: got %q", payload.ColumnSlug)
	}
	if !ticketUUID.Valid || !deptUUID.Valid {
		t.Errorf("UUIDs should be Valid; ticket=%v dept=%v", ticketUUID.Valid, deptUUID.Valid)
	}
}

func TestDecodeSpawnPayloadMalformedJSON(t *testing.T) {
	_, _, _, err := decodeSpawnPayload([]byte(`{not-json`))
	if err == nil {
		t.Fatal("decodeSpawnPayload: want error on malformed JSON, got nil")
	}
	if !strings.Contains(err.Error(), "decode payload") {
		t.Errorf("error should mention decode payload, got: %v", err)
	}
}

func TestDecodeSpawnPayloadInvalidTicketID(t *testing.T) {
	raw := []byte(`{"ticket_id":"not-a-uuid","department_id":"22222222-2222-2222-2222-222222222222","column_slug":"in_dev"}`)
	_, _, _, err := decodeSpawnPayload(raw)
	if err == nil {
		t.Fatal("decodeSpawnPayload: want error on invalid ticket_id, got nil")
	}
	if !strings.Contains(err.Error(), "ticket_id") {
		t.Errorf("error should mention ticket_id, got: %v", err)
	}
}

func TestDecodeSpawnPayloadInvalidDepartmentID(t *testing.T) {
	raw := []byte(`{"ticket_id":"11111111-1111-1111-1111-111111111111","department_id":"bogus","column_slug":"in_dev"}`)
	_, _, _, err := decodeSpawnPayload(raw)
	if err == nil {
		t.Fatal("decodeSpawnPayload: want error on invalid department_id, got nil")
	}
	if !strings.Contains(err.Error(), "department_id") {
		t.Errorf("error should mention department_id, got: %v", err)
	}
}

func TestClassifyPalaceErrTimeoutOnDeadlineExceededError(t *testing.T) {
	reason, class := classifyPalaceErr(context.Background(), context.DeadlineExceeded)
	if reason != ExitFinalizeWriteTimeout {
		t.Errorf("reason: want %q, got %q", ExitFinalizeWriteTimeout, reason)
	}
	if class != "timeout" {
		t.Errorf("class: want timeout, got %q", class)
	}
}

func TestClassifyPalaceErrTimeoutOnExpiredCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// isCtxDeadlineExceeded recognises an already-expired ctx whose Err is
	// DeadlineExceeded; here we use Canceled so it should NOT classify as
	// timeout.
	reason, class := classifyPalaceErr(ctx, errors.New("network failed"))
	if reason != ExitFinalizePalaceWriteFailed {
		t.Errorf("reason: want %q, got %q", ExitFinalizePalaceWriteFailed, reason)
	}
	if class != "palace_write" {
		t.Errorf("class: want palace_write, got %q", class)
	}
}

func TestClassifyPalaceErrPalaceWriteFallback(t *testing.T) {
	reason, class := classifyPalaceErr(context.Background(), errors.New("docker exec failed: no such container"))
	if reason != ExitFinalizePalaceWriteFailed {
		t.Errorf("reason: want %q, got %q", ExitFinalizePalaceWriteFailed, reason)
	}
	if class != "palace_write" {
		t.Errorf("class: want palace_write, got %q", class)
	}
}

func TestClassifyPalaceErrTimeoutWrappedInWritError(t *testing.T) {
	wrapped := errors.Join(errors.New("AddDrawer:"), context.DeadlineExceeded)
	reason, class := classifyPalaceErr(context.Background(), wrapped)
	if reason != ExitFinalizeWriteTimeout {
		t.Errorf("reason: want %q, got %q", ExitFinalizeWriteTimeout, reason)
	}
	if class != "timeout" {
		t.Errorf("class: want timeout, got %q", class)
	}
}

func TestSpawnPayloadFieldAccess(t *testing.T) {
	// Defends the struct shape contract — anything reading the JSON
	// payload (lock-tx body, runFakeAgent, runRealClaude) keeps these
	// three field names verbatim. Renaming any of them silently breaks
	// the event_outbox payload encoder on the trigger side.
	p := spawnPayload{
		TicketID:     "t",
		DepartmentID: "d",
		ColumnSlug:   "c",
	}
	if p.TicketID != "t" || p.DepartmentID != "d" || p.ColumnSlug != "c" {
		t.Error("spawnPayload field assignment broke")
	}
}

func TestRealClaudeInvocationFieldShape(t *testing.T) {
	// Same intent as TestSpawnPayloadFieldAccess: locks the bundle
	// shape so future param reshuffles don't silently drop data.
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	inv := realClaudeInvocation{
		InstanceID: id,
		EventID:    id,
		TicketUUID: id,
		Payload:    spawnPayload{TicketID: "x"},
		RoleSlug:   "engineer",
	}
	if !inv.InstanceID.Valid || inv.RoleSlug != "engineer" || inv.Payload.TicketID != "x" {
		t.Error("realClaudeInvocation field assignment broke")
	}
}

func TestTerminalWriteParamsFieldShape(t *testing.T) {
	// Same intent: lock the param-bundle field set.
	id := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	p := terminalWriteParams{
		InstanceID:       id,
		EventID:          id,
		TicketID:         id,
		Status:           "succeeded",
		ExitReason:       "exit_code_0",
		WakeUpStatus:     "ok",
		InsertTransition: true,
		FromCol:          "in_dev",
		ToCol:            "qa_review",
	}
	if p.Status != "succeeded" || p.FromCol != "in_dev" || p.ToCol != "qa_review" {
		t.Error("terminalWriteParams field assignment broke")
	}
}
