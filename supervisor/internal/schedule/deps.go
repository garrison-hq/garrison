// Package schedule implements the M9 scheduled-wake-up core: the
// bounded schedule-expression grammar (FR-103), the two-variable
// objective/acceptance templates (FR-107), task validation (FR-105),
// and the tick loop + firing transaction
// (specs/021-m9-scheduled-wakeups). Grammar and templates are pure
// stdlib — no schedule-parsing dependency (AGENTS.md locked-deps
// soft rule).
package schedule

import (
	"log/slog"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run-outcome constants mirror the SQL CHECK constraint on
// scheduled_task_runs.outcome (FR-108). Every firing attempt writes
// exactly one of these.
//
// OutcomeGateDeferred is asymmetric by design: for ticket mode it is
// terminal for the slot (the dept-weekly gate rejected the firing at
// fire time — verb-level rejection precedent); for oneshot mode it
// is NON-terminal (the spawn-prep gate deferred, processed_at stays
// NULL, and a later successful poll re-dispatch clears the run back
// to OutcomeFired before the instance insert, per FR-401).
const (
	OutcomeFired          = "fired"
	OutcomeSkippedOverlap = "skipped_overlap"
	OutcomeGateDeferred   = "gate_deferred"
	OutcomeFailed         = "failed"
)

// Deps carries the runtime configuration the schedule package needs.
// Constructed once at supervisor boot in cmd/supervisor/main.go and
// passed into RunLoop.
type Deps struct {
	Pool    *pgxpool.Pool
	Queries *store.Queries
	Logger  *slog.Logger

	// TickInterval is the loop cadence (GARRISON_SCHED_TICK_INTERVAL,
	// default 30s). A slot is on time if it fires within one tick of
	// its scheduled time (FR-102).
	TickInterval time.Duration

	// ClaimLimit bounds the per-tick claim transaction size; zero
	// means the default of 20. Remaining due tasks claim on the next
	// tick (worst-case one-tick slip, within FR-102's tolerance).
	ClaimLimit int

	// Throttle supplies the M8 dept-weekly gate + evidence writers
	// used by ticket-mode firing (FR-402).
	Throttle throttle.Deps

	// Now is the time source. Defaults to time.Now in production;
	// tests inject a deterministic clock.
	Now func() time.Time
}
