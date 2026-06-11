package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ChannelOneshotDue is the pg_notify channel emitted when an oneshot
// firing's event_outbox row lands. The dispatcher (M9 T009) routes it
// to spawn.SpawnOneshot. The name contains dots, so every LISTEN
// statement must double-quote it (M6 retro gotcha 3).
const ChannelOneshotDue = "work.scheduled.oneshot_due"

// insertOneshotOutboxSQL inserts the oneshot due-event row, reusing
// the trigger-era outbox insert shape (M2.1 emit_ticket_created). The
// channel literal is baked in — call sites never assemble it (M6
// retro gotcha 3). Raw SQL because the generic outbox insert lives in
// SQL trigger functions, not the sqlc query set (mcpserverwork
// precedent for tx-level raw queries).
const insertOneshotOutboxSQL = `INSERT INTO event_outbox (channel, payload) VALUES ('work.scheduled.oneshot_due', $1) RETURNING id`

// oneshotDuePayload is the work.scheduled.oneshot_due envelope (plan
// §sqlc): the dispatcher parses event_id exactly as for every other
// channel; SpawnOneshot resolves the rest through the outbox row. The
// event_outbox row's own payload omits event_id (the row id IS the
// event id, unknowable before the INSERT returns); the notify body
// carries all four fields.
type oneshotDuePayload struct {
	EventID            string `json:"event_id,omitempty"`
	ScheduledTaskRunID string `json:"scheduled_task_run_id"`
	RoleSlug           string `json:"role_slug"`
	DepartmentID       string `json:"department_id"`
}

// fireTicketMode fires one ticket-mode slot inside the tick tx (plan
// §1 step 2): dept-weekly gate (FR-402, M8 function reused) → on
// reject, a gate_deferred run row + throttle.FireDeptWeekly evidence
// (terminal for the slot — verb-level rejection precedent); on pass,
// rendered templates → InsertScheduledTicket, whose INSERT trigger
// emits the outbox row + the existing work.ticket.created.<dept>.todo
// notify in this same tx (FR-200), → a fired run row carrying the
// ticket anchor (FR-201). The M6 company throttle fires later at the
// ticket's normal spawn-prep — existing behavior, not duplicated here
// (FR-400 ordering).
func fireTicketMode(ctx context.Context, q *store.Queries, task store.ScheduledTask, now time.Time) (string, error) {
	decision, err := throttle.CheckDeptWeekly(ctx, q, task.DepartmentID)
	if err != nil {
		return "", fmt.Errorf("schedule: CheckDeptWeekly: %w", err)
	}
	if !decision.Allowed {
		detail := deptWeeklyDeferDetail(decision)
		if _, err := insertRun(ctx, q, task, OutcomeGateDeferred, &detail, pgtype.UUID{}); err != nil {
			return "", err
		}
		// throttle_events.company_id is required by M6's schema;
		// resolve it on the reject path only.
		dept, err := q.GetDepartmentByID(ctx, task.DepartmentID)
		if err != nil {
			return "", fmt.Errorf("schedule: GetDepartmentByID: %w", err)
		}
		if err := throttle.FireDeptWeekly(ctx, q, dept.CompanyID, decision, task.DepartmentID, uuidString(task.ID)); err != nil {
			return "", fmt.Errorf("schedule: FireDeptWeekly: %w", err)
		}
		return OutcomeGateDeferred, nil
	}

	// {{fire_at}} renders as this firing's timestamp — the same value
	// AdvanceScheduledTask persists to last_fired_at, so the next
	// firing's {{last_fired_at}} equals this one's {{fire_at}}
	// (FR-107 chain consistency).
	objective := RenderTemplate(task.ObjectiveTemplate, now, task.LastFiredAt)
	acceptance := RenderTemplate(task.AcceptanceCriteriaTemplate, now, task.LastFiredAt)
	ticket, err := q.InsertScheduledTicket(ctx, store.InsertScheduledTicketParams{
		DepartmentID:       task.DepartmentID,
		Objective:          objective,
		AcceptanceCriteria: &acceptance,
		RoleSlug:           task.RoleSlug,
	})
	if err != nil {
		return "", fmt.Errorf("schedule: InsertScheduledTicket: %w", err)
	}
	if _, err := insertRun(ctx, q, task, OutcomeFired, nil, ticket.ID); err != nil {
		return "", err
	}
	return OutcomeFired, nil
}

// fireOneshotMode fires one oneshot slot inside the tick tx (plan §1
// step 3): a fired run row + the event_outbox row + NotifyOneshotDue.
// No spawn happens here — the dispatcher consumes the notify (or the
// poll fallback finds the unprocessed row) and runs spawn.SpawnOneshot
// outside the tick tx; the M6 company gates run at spawn-prep,
// matching where reactive spawns gate (FR-400/FR-401).
func fireOneshotMode(ctx context.Context, q *store.Queries, tx pgx.Tx, task store.ScheduledTask) (string, error) {
	run, err := insertRun(ctx, q, task, OutcomeFired, nil, pgtype.UUID{})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(oneshotDuePayload{
		ScheduledTaskRunID: uuidString(run.ID),
		RoleSlug:           task.RoleSlug,
		DepartmentID:       uuidString(task.DepartmentID),
	})
	if err != nil {
		return "", fmt.Errorf("schedule: marshal oneshot outbox payload: %w", err)
	}
	var eventID pgtype.UUID
	if err := tx.QueryRow(ctx, insertOneshotOutboxSQL, body).Scan(&eventID); err != nil {
		return "", fmt.Errorf("schedule: insert oneshot outbox row: %w", err)
	}
	notify, err := json.Marshal(oneshotDuePayload{
		EventID:            uuidString(eventID),
		ScheduledTaskRunID: uuidString(run.ID),
		RoleSlug:           task.RoleSlug,
		DepartmentID:       uuidString(task.DepartmentID),
	})
	if err != nil {
		return "", fmt.Errorf("schedule: marshal oneshot notify payload: %w", err)
	}
	if err := q.NotifyOneshotDue(ctx, string(notify)); err != nil {
		return "", fmt.Errorf("schedule: NotifyOneshotDue: %w", err)
	}
	return OutcomeFired, nil
}

// insertRun writes the per-slot run record (FR-108). slot_at is the
// claimed next_fire_at — the slot this run answers for, which after a
// recovery collapse may be well in the past while fired_at is now.
func insertRun(ctx context.Context, q *store.Queries, task store.ScheduledTask, outcome string, detail *string, ticketID pgtype.UUID) (store.InsertScheduledTaskRunRow, error) {
	row, err := q.InsertScheduledTaskRun(ctx, store.InsertScheduledTaskRunParams{
		ScheduledTaskID: task.ID,
		SlotAt:          task.NextFireAt,
		Outcome:         outcome,
		Detail:          detail,
		TicketID:        ticketID,
	})
	if err != nil {
		return store.InsertScheduledTaskRunRow{}, fmt.Errorf("schedule: InsertScheduledTaskRun(%s): %w", outcome, err)
	}
	return row, nil
}

// deptWeeklyDeferDetail renders the human-readable reason recorded on
// a ticket-mode gate_deferred run row (FR-402).
func deptWeeklyDeferDetail(decision throttle.DeptWeeklyDecision) string {
	budget := int32(0)
	if decision.Budget != nil {
		budget = *decision.Budget
	}
	return fmt.Sprintf(
		"department %q weekly ticket budget exceeded (current=%d, budget=%d); slot deferred (FR-402)",
		decision.DepartmentSlug, decision.CurrentCount, budget,
	)
}

// uuidString renders a pgtype.UUID as the canonical 8-4-4-4-12 hex
// string. Duplicated deliberately from internal/events to keep
// schedule free of an events import (four lines of fmt is cheaper
// than a shared util package — events/poller.go precedent).
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}
