package schedule

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestOneshotDuePayloadShapes pins the work.scheduled.oneshot_due
// envelope (plan §sqlc): the outbox row's payload omits event_id (the
// row id IS the event id), while the notify body carries all four
// fields the dispatcher contract names.
func TestOneshotDuePayloadShapes(t *testing.T) {
	outbox, err := json.Marshal(oneshotDuePayload{
		ScheduledTaskRunID: "11111111-2222-3333-4444-555555555555",
		RoleSlug:           "engineer",
		DepartmentID:       "66666666-7777-8888-9999-aaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatalf("marshal outbox payload: %v", err)
	}
	var outboxFields map[string]string
	if err := json.Unmarshal(outbox, &outboxFields); err != nil {
		t.Fatalf("unmarshal outbox payload: %v", err)
	}
	if _, present := outboxFields["event_id"]; present {
		t.Fatalf("outbox payload carries event_id, want omitted: %s", outbox)
	}
	for _, key := range []string{"scheduled_task_run_id", "role_slug", "department_id"} {
		if outboxFields[key] == "" {
			t.Fatalf("outbox payload missing %q: %s", key, outbox)
		}
	}

	notify, err := json.Marshal(oneshotDuePayload{
		EventID:            "00000000-0000-4000-8000-000000000000",
		ScheduledTaskRunID: "11111111-2222-3333-4444-555555555555",
		RoleSlug:           "engineer",
		DepartmentID:       "66666666-7777-8888-9999-aaaaaaaaaaaa",
	})
	if err != nil {
		t.Fatalf("marshal notify payload: %v", err)
	}
	var notifyFields map[string]string
	if err := json.Unmarshal(notify, &notifyFields); err != nil {
		t.Fatalf("unmarshal notify payload: %v", err)
	}
	for _, key := range []string{"event_id", "scheduled_task_run_id", "role_slug", "department_id"} {
		if notifyFields[key] == "" {
			t.Fatalf("notify payload missing %q: %s", key, notify)
		}
	}
}

// TestDeptWeeklyDeferDetailRendersDecision pins the human-readable
// gate_deferred reason, including the nil-budget (unlimited) rendering
// that should never fire in practice but must not panic.
func TestDeptWeeklyDeferDetailRendersDecision(t *testing.T) {
	budget := int32(2)
	detail := deptWeeklyDeferDetail(throttle.DeptWeeklyDecision{
		CurrentCount:   2,
		Budget:         &budget,
		DepartmentSlug: "engineering",
	})
	for _, want := range []string{`"engineering"`, "current=2", "budget=2", "FR-402"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail %q does not contain %q", detail, want)
		}
	}

	nilBudget := deptWeeklyDeferDetail(throttle.DeptWeeklyDecision{DepartmentSlug: "ops"})
	if !strings.Contains(nilBudget, "budget=0") {
		t.Fatalf("nil-budget detail %q does not render budget=0", nilBudget)
	}
}

// TestUUIDStringCanonicalForm pins the 8-4-4-4-12 rendering plus the
// empty-string contract for NULL UUIDs.
func TestUUIDStringCanonicalForm(t *testing.T) {
	u := pgtype.UUID{
		Bytes: [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef},
		Valid: true,
	}
	if got, want := uuidString(u), "01234567-89ab-cdef-0123-456789abcdef"; got != want {
		t.Fatalf("uuidString = %q, want %q", got, want)
	}
	if got := uuidString(pgtype.UUID{}); got != "" {
		t.Fatalf("uuidString(NULL) = %q, want empty", got)
	}
}
