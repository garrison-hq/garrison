//go:build integration

package spawn

// M9 T007 integration suite: SpawnOneshot's prep transaction against a
// real Postgres — gate defer (FR-400/FR-401), poll re-dispatch clearing
// gate_deferred back to fired (FR-401), and the origin instance row
// (agent_instances_exactly_one_origin: scheduled_task_run_id set,
// ticket_id NULL). The run branch rides the M1 fake-agent harness
// (deps.FakeAgentCmd) exactly like Spawn's own integration suites.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// oneshotFixture is the seeded (company → department → scheduled task →
// run → outbox event) chain SpawnOneshot consumes.
type oneshotFixture struct {
	eventID   pgtype.UUID
	runID     pgtype.UUID
	taskID    pgtype.UUID
	deptID    pgtype.UUID
	companyID pgtype.UUID
}

// seedOneshot inserts the fixture chain. pauseUntilDelta sets
// companies.pause_until relative to now (nil leaves it NULL; a negative
// delta seeds an expired pause window). runOutcome seeds the run row's
// starting outcome ('fired' for fresh dispatches, 'gate_deferred' for
// the FR-401 retry path).
func seedOneshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool, pauseUntilDelta *time.Duration, runOutcome string) oneshotFixture {
	t.Helper()
	var fx oneshotFixture

	pauseSQL := "NULL"
	if pauseUntilDelta != nil {
		pauseSQL = fmt.Sprintf("NOW() + INTERVAL '%d seconds'", int((*pauseUntilDelta).Seconds()))
	}
	if err := pool.QueryRow(ctx, fmt.Sprintf(
		`INSERT INTO companies (id, name, pause_until) VALUES (gen_random_uuid(), 'oneshot-test-co', %s) RETURNING id`,
		pauseSQL,
	)).Scan(&fx.companyID); err != nil {
		t.Fatalf("insert company: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO departments (id, company_id, slug, name, concurrency_cap, workspace_path)
		VALUES (gen_random_uuid(), $1, 'engineering', 'Engineering', 5, $2)
		RETURNING id`,
		fx.companyID, t.TempDir(),
	).Scan(&fx.deptID); err != nil {
		t.Fatalf("insert department: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO scheduled_tasks (
			name, department_id, role_slug, mode, schedule_expr,
			next_fire_at, objective_template, acceptance_criteria_template, last_fired_at
		) VALUES ($1, $2, 'engineer', 'oneshot', 'every@30m',
			NOW() + INTERVAL '30 minutes',
			'Summarize activity since {{last_fired_at}}.',
			'Digest posted for the slot at {{fire_at}}.',
			NOW())
		RETURNING id`,
		t.Name(), fx.deptID,
	).Scan(&fx.taskID); err != nil {
		t.Fatalf("insert scheduled task: %v", err)
	}

	var detail *string
	if runOutcome == "gate_deferred" {
		d := "seeded prior gate defer"
		detail = &d
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO scheduled_task_runs (scheduled_task_id, slot_at, outcome, detail)
		VALUES ($1, NOW(), $2, $3)
		RETURNING id`,
		fx.taskID, runOutcome, detail,
	).Scan(&fx.runID); err != nil {
		t.Fatalf("insert run: %v", err)
	}

	payload := fmt.Sprintf(
		`{"scheduled_task_run_id":"%s","role_slug":"engineer","department_id":"%s"}`,
		uuidString(fx.runID), uuidString(fx.deptID),
	)
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.scheduled.oneshot_due', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&fx.eventID); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	return fx
}

func oneshotDeps(pool *pgxpool.Pool, fakeCmd string) Deps {
	var defaultCost pgtype.Numeric
	_ = defaultCost.Scan("0.10")
	return Deps{
		Pool:              pool,
		Queries:           store.New(pool),
		Logger:            slog.New(slog.DiscardHandler),
		SubprocessTimeout: 30 * time.Second,
		FakeAgentCmd:      fakeCmd,
		Throttle: throttle.Deps{
			Pool:                pool,
			Logger:              slog.New(slog.DiscardHandler),
			DefaultSpawnCostUSD: defaultCost,
			RateLimitBackOff:    60 * time.Second,
			Now:                 time.Now,
		},
	}
}

type oneshotRunRow struct {
	Outcome         string
	Detail          *string
	AgentInstanceID pgtype.UUID
}

func readRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID pgtype.UUID) oneshotRunRow {
	t.Helper()
	var r oneshotRunRow
	if err := pool.QueryRow(ctx,
		`SELECT outcome, detail, agent_instance_id FROM scheduled_task_runs WHERE id = $1`, runID,
	).Scan(&r.Outcome, &r.Detail, &r.AgentInstanceID); err != nil {
		t.Fatalf("read run: %v", err)
	}
	return r
}

// TestSpawnOneshotGateDeferUpdatesRun — paused company → SpawnOneshot
// defers: run outcome gate_deferred with a detail, processed_at stays
// NULL (the poll fallback re-checks after the window, FR-401), and no
// agent_instances row lands.
func TestSpawnOneshotGateDeferUpdatesRun(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	pause := 120 * time.Second
	fx := seedOneshot(t, ctx, pool, &pause, "fired")

	deps := oneshotDeps(pool, "")
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("SpawnOneshot err = %v; want nil (defer is not an error)", err)
	}

	run := readRun(t, ctx, pool, fx.runID)
	if run.Outcome != "gate_deferred" {
		t.Errorf("run outcome = %q; want gate_deferred", run.Outcome)
	}
	if run.Detail == nil || *run.Detail == "" {
		t.Error("run detail should carry the defer reason; got empty")
	}
	if run.AgentInstanceID.Valid {
		t.Errorf("run.agent_instance_id should be NULL after defer; got %s", uuidString(run.AgentInstanceID))
	}

	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, fx.eventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox: %v", err)
	}
	if processed.Valid {
		t.Errorf("event_outbox.processed_at should remain NULL after defer; got %v", processed.Time)
	}

	var instances int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_instances WHERE department_id = $1`, fx.deptID,
	).Scan(&instances); err != nil {
		t.Fatalf("count agent_instances: %v", err)
	}
	if instances != 0 {
		t.Errorf("agent_instances count = %d; want 0 after gate defer", instances)
	}
}

// TestSpawnOneshotRetryAfterGateClearsToFired — a previously deferred
// run plus an expired pause window: the poll re-dispatch succeeds, the
// run outcome clears back to fired BEFORE the instance insert (FR-401 —
// gate_deferred is non-terminal for oneshot), and the origin instance
// row lands.
func TestSpawnOneshotRetryAfterGateClearsToFired(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	expired := -60 * time.Second
	fx := seedOneshot(t, ctx, pool, &expired, "gate_deferred")

	deps := oneshotDeps(pool, "/bin/echo oneshot-retry")
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("SpawnOneshot err = %v; want nil", err)
	}

	run := readRun(t, ctx, pool, fx.runID)
	if run.Outcome != "fired" {
		t.Errorf("run outcome = %q; want fired after successful re-dispatch", run.Outcome)
	}
	if run.Detail != nil {
		t.Errorf("run detail = %q; want NULL after clearing back to fired", *run.Detail)
	}
	if !run.AgentInstanceID.Valid {
		t.Fatal("run.agent_instance_id should be backfilled after the instance insert")
	}

	var originRunID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT scheduled_task_run_id FROM agent_instances WHERE id = $1`, run.AgentInstanceID,
	).Scan(&originRunID); err != nil {
		t.Fatalf("read agent_instances origin: %v", err)
	}
	if uuidString(originRunID) != uuidString(fx.runID) {
		t.Errorf("instance scheduled_task_run_id = %s; want %s", uuidString(originRunID), uuidString(fx.runID))
	}
}

// TestSpawnOneshotInsertsOriginInstance — the happy path's instance row
// carries the oneshot origin: scheduled_task_run_id set, ticket_id
// NULL, the agent_instances_exactly_one_origin CHECK satisfied, and the
// run row backfilled with the instance anchor (plan decision 5).
func TestSpawnOneshotInsertsOriginInstance(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	fx := seedOneshot(t, ctx, pool, nil, "fired")

	deps := oneshotDeps(pool, "/bin/echo oneshot-ok")
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("SpawnOneshot err = %v; want nil", err)
	}

	run := readRun(t, ctx, pool, fx.runID)
	if run.Outcome != "fired" {
		t.Errorf("run outcome = %q; want fired (instance terminal state is the completion surface)", run.Outcome)
	}
	if !run.AgentInstanceID.Valid {
		t.Fatal("run.agent_instance_id should be backfilled at spawn")
	}

	var (
		ticketID pgtype.UUID
		originID pgtype.UUID
		roleSlug string
		status   string
	)
	if err := pool.QueryRow(ctx, `
		SELECT ticket_id, scheduled_task_run_id, role_slug, status
		  FROM agent_instances WHERE id = $1`, run.AgentInstanceID,
	).Scan(&ticketID, &originID, &roleSlug, &status); err != nil {
		t.Fatalf("read agent_instances: %v", err)
	}
	if ticketID.Valid {
		t.Errorf("instance ticket_id = %s; want NULL for oneshot origin", uuidString(ticketID))
	}
	if uuidString(originID) != uuidString(fx.runID) {
		t.Errorf("instance scheduled_task_run_id = %s; want %s", uuidString(originID), uuidString(fx.runID))
	}
	if roleSlug != "engineer" {
		t.Errorf("instance role_slug = %q; want engineer", roleSlug)
	}
	if status != "succeeded" {
		t.Errorf("instance status = %q; want succeeded (fake agent exit 0)", status)
	}

	// Dedupe: a second dispatch of the same event is a no-op.
	if err := SpawnOneshot(ctx, deps, fx.eventID); err != nil {
		t.Fatalf("second SpawnOneshot err = %v; want nil", err)
	}
	var instances int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM agent_instances WHERE department_id = $1`, fx.deptID,
	).Scan(&instances); err != nil {
		t.Fatalf("count agent_instances: %v", err)
	}
	if instances != 1 {
		t.Errorf("agent_instances count = %d; want 1 (LockEventForProcessing dedupe)", instances)
	}
}

// -----------------------------------------------------------------------
// M9 T008 — WriteFinalizeOneshot (the supervisor-side finalize commit)
// -----------------------------------------------------------------------

// oneshotFailingPalaceExec is the sad-path dockerexec seam: every Run
// errors, so AddDrawer fails inside WriteFinalizeOneshot's atomic
// bracket and the whole tx must roll back.
type oneshotFailingPalaceExec struct{}

func (oneshotFailingPalaceExec) Run(_ context.Context, _ []string, stdin io.Reader) ([]byte, []byte, error) {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	return nil, nil, errors.New("palace sidecar down")
}

func (oneshotFailingPalaceExec) RunStream(
	_ context.Context,
	_ []string,
	_ func(stdin io.WriteCloser) error,
	_ func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	return nil, errors.New("oneshotFailingPalaceExec: RunStream not implemented")
}

// seedOneshotRunningInstance inserts the running oneshot-origin
// instance row prepareOneshot would have created (scheduled_task_run_id
// set, ticket_id NULL) and backfills the run anchor — the state
// WriteFinalizeOneshot finds when the pipeline fires onCommit.
func seedOneshotRunningInstance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fx oneshotFixture) pgtype.UUID {
	t.Helper()
	var instanceID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agent_instances (department_id, scheduled_task_run_id, status, role_slug)
		 VALUES ($1, $2, 'running', 'engineer') RETURNING id`,
		fx.deptID, fx.runID,
	).Scan(&instanceID); err != nil {
		t.Fatalf("insert running oneshot instance: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE scheduled_task_runs SET agent_instance_id = $1 WHERE id = $2`,
		instanceID, fx.runID,
	); err != nil {
		t.Fatalf("backfill run agent_instance_id: %v", err)
	}
	return instanceID
}

// seedOneshotSiblingRun inserts a second fired run + outbox event on an
// already-seeded task (one company/department/task chain per pool — the
// companies unique constraint forbids re-seeding the full fixture).
func seedOneshotSiblingRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, base oneshotFixture) oneshotFixture {
	t.Helper()
	fx := base
	if err := pool.QueryRow(ctx, `
		INSERT INTO scheduled_task_runs (scheduled_task_id, slot_at, outcome)
		VALUES ($1, NOW(), 'fired')
		RETURNING id`,
		fx.taskID,
	).Scan(&fx.runID); err != nil {
		t.Fatalf("insert sibling run: %v", err)
	}
	payload := fmt.Sprintf(
		`{"scheduled_task_run_id":"%s","role_slug":"engineer","department_id":"%s"}`,
		uuidString(fx.runID), uuidString(fx.deptID),
	)
	if err := pool.QueryRow(ctx,
		`INSERT INTO event_outbox (channel, payload) VALUES ('work.scheduled.oneshot_due', $1::jsonb) RETURNING id`,
		payload,
	).Scan(&fx.eventID); err != nil {
		t.Fatalf("insert sibling event: %v", err)
	}
	return fx
}

// oneshotFinalizeDeps wires the base oneshot deps plus a palace client
// riding the given dockerexec seam (the m7.1 fake-palace pattern).
func oneshotFinalizeDeps(pool *pgxpool.Pool, palaceExec dockerexec.DockerExec) Deps {
	deps := oneshotDeps(pool, "")
	deps.Palace = &mempalace.Client{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		Timeout:            5 * time.Second,
		Exec:               palaceExec,
	}
	deps.FinalizeWriteTimeout = 10 * time.Second
	return deps
}

// oneshotFinalizePayload is a validated-shape finalize_oneshot payload:
// outcome ≥ 10 chars, rationale ≥ 50, exactly one KG triple (the
// m71FakePalaceExec answers ids 1+2, the one-triple AddTriples shape).
func oneshotFinalizePayload() finalize.OneshotPayload {
	return finalize.OneshotPayload{
		Outcome: "Weekly digest compiled and posted for the engineering department",
		DiaryEntry: finalize.DiaryEntry{
			Rationale: strings.Repeat("Scanned the week's activity and summarized the notable threads for the operator. ", 3),
			Artifacts: []string{"digest.md"},
			Blockers:  []string{},
			Discoveries: []string{
				"activity clusters on Mondays",
			},
		},
		KGTriples: []finalize.KGTriple{
			{Subject: "weekly-digest", Predicate: "covers", Object: "engineering", ValidFrom: time.Now().UTC()},
		},
	}
}

// oneshotStructuredOutcomeDoc mirrors the persisted structured_outcome
// JSONB shape for assertions.
type oneshotStructuredOutcomeDoc struct {
	Outcome    string `json:"outcome"`
	DiaryEntry struct {
		Rationale string `json:"rationale"`
	} `json:"diary_entry"`
	KGTriples []struct {
		Subject   string `json:"subject"`
		Predicate string `json:"predicate"`
		Object    string `json:"object"`
	} `json:"kg_triples"`
	Verification struct {
		DiaryLength        int  `json:"diary_length"`
		ThinDiaryThreshold int  `json:"thin_diary_threshold"`
		ThinDiary          bool `json:"thin_diary"`
		KGTripleCount      int  `json:"kg_triple_count"`
		MissingKGFacts     bool `json:"missing_kg_facts"`
	} `json:"verification"`
}

func readStructuredOutcome(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runID pgtype.UUID) []byte {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx,
		`SELECT structured_outcome FROM scheduled_task_runs WHERE id = $1`, runID,
	).Scan(&raw); err != nil {
		t.Fatalf("read structured_outcome: %v", err)
	}
	return raw
}

// TestWriteFinalizeOneshotCommitsAtomically — the payload commit writes
// structured_outcome (payload + verification sub-object), the terminal
// succeeded instance row, and the event-outbox processed mark in one
// tx; a palace failure inside the bracket rolls back every DB write and
// records the failed terminal instance outside the tx.
func TestWriteFinalizeOneshotCommitsAtomically(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	fx := seedOneshot(t, ctx, pool, nil, "fired")

	t.Run("happy_path_one_tx", func(t *testing.T) {
		instanceID := seedOneshotRunningInstance(t, ctx, pool, fx)
		palaceExec := &m71FakePalaceExec{}
		deps := oneshotFinalizeDeps(pool, palaceExec)

		if err := WriteFinalizeOneshot(ctx, deps, fx.runID, instanceID, oneshotFinalizePayload(), OneshotTerminal{}); err != nil {
			t.Fatalf("WriteFinalizeOneshot err = %v; want nil", err)
		}

		// structured_outcome: full payload + verification sub-object.
		raw := readStructuredOutcome(t, ctx, pool, fx.runID)
		if len(raw) == 0 {
			t.Fatal("structured_outcome is NULL; want the committed payload document")
		}
		var doc oneshotStructuredOutcomeDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode structured_outcome: %v", err)
		}
		if want := oneshotFinalizePayload().Outcome; doc.Outcome != want {
			t.Errorf("structured_outcome.outcome = %q; want %q", doc.Outcome, want)
		}
		if doc.DiaryEntry.Rationale == "" {
			t.Error("structured_outcome.diary_entry.rationale is empty; want the payload rationale")
		}
		if len(doc.KGTriples) != 1 || doc.KGTriples[0].Subject != "weekly-digest" {
			t.Errorf("structured_outcome.kg_triples = %+v; want the one payload triple", doc.KGTriples)
		}
		v := doc.Verification
		if v.DiaryLength <= 0 {
			t.Errorf("verification.diary_length = %d; want > 0", v.DiaryLength)
		}
		if v.ThinDiaryThreshold != 200 {
			t.Errorf("verification.thin_diary_threshold = %d; want 200", v.ThinDiaryThreshold)
		}
		if got, want := v.ThinDiary, v.DiaryLength < v.ThinDiaryThreshold; got != want {
			t.Errorf("verification.thin_diary = %v; want %v (diary_length %d vs threshold %d)",
				got, want, v.DiaryLength, v.ThinDiaryThreshold)
		}
		if v.KGTripleCount != 1 {
			t.Errorf("verification.kg_triple_count = %d; want 1", v.KGTripleCount)
		}
		if v.MissingKGFacts {
			t.Error("verification.missing_kg_facts = true; want false (one triple committed)")
		}

		// Terminal instance row committed inside the same tx.
		var (
			status     string
			exitReason *string
			finished   pgtype.Timestamptz
		)
		if err := pool.QueryRow(ctx,
			`SELECT status, exit_reason, finished_at FROM agent_instances WHERE id = $1`, instanceID,
		).Scan(&status, &exitReason, &finished); err != nil {
			t.Fatalf("read agent_instances: %v", err)
		}
		if status != "succeeded" {
			t.Errorf("instance status = %q; want succeeded", status)
		}
		if exitReason == nil || *exitReason != ExitCompleted {
			t.Errorf("instance exit_reason = %v; want %q", exitReason, ExitCompleted)
		}
		if !finished.Valid {
			t.Error("instance finished_at is NULL; want set by the terminal write")
		}

		// Run row keeps outcome='fired' (decision 5) and the event is
		// marked processed inside the tx (no poll re-dispatch).
		if run := readRun(t, ctx, pool, fx.runID); run.Outcome != "fired" {
			t.Errorf("run outcome = %q; want fired after finalize commit", run.Outcome)
		}
		var processed pgtype.Timestamptz
		if err := pool.QueryRow(ctx,
			`SELECT processed_at FROM event_outbox WHERE id = $1`, fx.eventID,
		).Scan(&processed); err != nil {
			t.Fatalf("read event_outbox: %v", err)
		}
		if !processed.Valid {
			t.Error("event_outbox.processed_at is NULL; want marked inside the atomic tx")
		}

		// Palace writes: AddDrawer + the one-triple AddTriples.
		if got := palaceExec.callCount(); got != 2 {
			t.Errorf("palace exec calls = %d; want 2 (AddDrawer + AddTriples)", got)
		}
	})

	t.Run("palace_failure_rolls_back_all_writes", func(t *testing.T) {
		fx := seedOneshotSiblingRun(t, ctx, pool, fx)
		instanceID := seedOneshotRunningInstance(t, ctx, pool, fx)
		deps := oneshotFinalizeDeps(pool, oneshotFailingPalaceExec{})

		err := WriteFinalizeOneshot(ctx, deps, fx.runID, instanceID, oneshotFinalizePayload(), OneshotTerminal{})
		if err == nil {
			t.Fatal("WriteFinalizeOneshot err = nil; want palace-write failure")
		}
		if !strings.Contains(err.Error(), ExitFinalizePalaceWriteFailed) {
			t.Errorf("err = %v; want the %s exit reason in the message", err, ExitFinalizePalaceWriteFailed)
		}

		// All tx writes rolled back: no structured_outcome, event still
		// unprocessed, run still fired.
		if raw := readStructuredOutcome(t, ctx, pool, fx.runID); len(raw) != 0 {
			t.Errorf("structured_outcome = %s; want NULL after rollback", raw)
		}
		var processed pgtype.Timestamptz
		if err := pool.QueryRow(ctx,
			`SELECT processed_at FROM event_outbox WHERE id = $1`, fx.eventID,
		).Scan(&processed); err != nil {
			t.Fatalf("read event_outbox: %v", err)
		}
		if processed.Valid {
			t.Error("event_outbox.processed_at set despite rollback; want NULL")
		}
		if run := readRun(t, ctx, pool, fx.runID); run.Outcome != "fired" {
			t.Errorf("run outcome = %q; want fired (failure state reads through the instance join)", run.Outcome)
		}

		// The failed terminal row lands outside the rolled-back tx.
		var (
			status     string
			exitReason *string
		)
		if err := pool.QueryRow(ctx,
			`SELECT status, exit_reason FROM agent_instances WHERE id = $1`, instanceID,
		).Scan(&status, &exitReason); err != nil {
			t.Fatalf("read agent_instances: %v", err)
		}
		if status != "failed" {
			t.Errorf("instance status = %q; want failed", status)
		}
		if exitReason == nil || *exitReason != ExitFinalizePalaceWriteFailed {
			t.Errorf("instance exit_reason = %v; want %q", exitReason, ExitFinalizePalaceWriteFailed)
		}
	})
}

// TestWriteFinalizeOneshotPersistsCostAndWakeup (M9 review #2): the
// commit-time OneshotTerminal metadata lands on the succeeded instance
// row — total_cost_usd (the spend the M6 budget gate sums) and
// wake_up_status, mirroring the ticket-mode terminal write shape.
func TestWriteFinalizeOneshotPersistsCostAndWakeup(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	fx := seedOneshot(t, ctx, pool, nil, "fired")
	instanceID := seedOneshotRunningInstance(t, ctx, pool, fx)
	palaceExec := &m71FakePalaceExec{}
	deps := oneshotFinalizeDeps(pool, palaceExec)

	var cost pgtype.Numeric
	if err := cost.Scan("0.4275"); err != nil {
		t.Fatalf("cost scan: %v", err)
	}
	if err := WriteFinalizeOneshot(ctx, deps, fx.runID, instanceID, oneshotFinalizePayload(), OneshotTerminal{
		Cost:         cost,
		WakeUpStatus: "ok",
	}); err != nil {
		t.Fatalf("WriteFinalizeOneshot err = %v; want nil", err)
	}

	var (
		gotCost   pgtype.Numeric
		gotWakeup *string
	)
	if err := pool.QueryRow(ctx,
		`SELECT total_cost_usd, wake_up_status FROM agent_instances WHERE id = $1`, instanceID,
	).Scan(&gotCost, &gotWakeup); err != nil {
		t.Fatalf("read agent_instances: %v", err)
	}
	if !gotCost.Valid {
		t.Fatal("total_cost_usd is NULL; want the commit-time cost (review #2)")
	}
	f, err := gotCost.Float64Value()
	if err != nil {
		t.Fatalf("Float64Value: %v", err)
	}
	// agent_instances.total_cost_usd is NUMERIC(10,6): 0.4275 roundtrips.
	if f.Float64 != 0.4275 {
		t.Errorf("total_cost_usd = %v; want 0.4275", f.Float64)
	}
	if gotWakeup == nil || *gotWakeup != "ok" {
		t.Errorf("wake_up_status = %v; want ok", gotWakeup)
	}
}

// TestWriteFinalizeOneshotThinDiaryThresholdOverride (M9 review #5):
// a configured Deps.ThinDiaryThreshold lands on the committed
// verification sub-object and drives the thin_diary evaluation — the
// end-to-end proof the bound is no longer the hardcoded 200.
func TestWriteFinalizeOneshotThinDiaryThresholdOverride(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	fx := seedOneshot(t, ctx, pool, nil, "fired")
	instanceID := seedOneshotRunningInstance(t, ctx, pool, fx)
	deps := oneshotFinalizeDeps(pool, &m71FakePalaceExec{})
	deps.ThinDiaryThreshold = 10000 // far above any test diary body

	if err := WriteFinalizeOneshot(ctx, deps, fx.runID, instanceID, oneshotFinalizePayload(), OneshotTerminal{}); err != nil {
		t.Fatalf("WriteFinalizeOneshot err = %v; want nil", err)
	}
	var doc oneshotStructuredOutcomeDoc
	if err := json.Unmarshal(readStructuredOutcome(t, ctx, pool, fx.runID), &doc); err != nil {
		t.Fatalf("decode structured_outcome: %v", err)
	}
	if doc.Verification.ThinDiaryThreshold != 10000 {
		t.Errorf("verification.thin_diary_threshold = %d; want the configured 10000", doc.Verification.ThinDiaryThreshold)
	}
	if !doc.Verification.ThinDiary {
		t.Errorf("verification.thin_diary = false; want true (diary_length %d < 10000)", doc.Verification.DiaryLength)
	}
}

// TestWriteFinalizeOneshotRejectsDoubleCommit — the FR-260-analog
// guard: a second commit for an already-finalized run errors without
// touching the committed state (no extra palace writes, structured
// outcome byte-identical, instance still succeeded).
func TestWriteFinalizeOneshotRejectsDoubleCommit(t *testing.T) {
	pool := testdb.Start(t)
	ctx := context.Background()
	fx := seedOneshot(t, ctx, pool, nil, "fired")
	instanceID := seedOneshotRunningInstance(t, ctx, pool, fx)
	palaceExec := &m71FakePalaceExec{}
	deps := oneshotFinalizeDeps(pool, palaceExec)

	if err := WriteFinalizeOneshot(ctx, deps, fx.runID, instanceID, oneshotFinalizePayload(), OneshotTerminal{}); err != nil {
		t.Fatalf("first WriteFinalizeOneshot err = %v; want nil", err)
	}
	committed := readStructuredOutcome(t, ctx, pool, fx.runID)

	err := WriteFinalizeOneshot(ctx, deps, fx.runID, instanceID, oneshotFinalizePayload(), OneshotTerminal{})
	if err == nil {
		t.Fatal("second WriteFinalizeOneshot err = nil; want double-commit rejection")
	}
	if !strings.Contains(err.Error(), "double commit") {
		t.Errorf("err = %v; want a double-commit rejection", err)
	}

	// No second palace write fired (the guard runs before AddDrawer).
	if got := palaceExec.callCount(); got != 2 {
		t.Errorf("palace exec calls = %d; want 2 (first commit only)", got)
	}
	// Committed state untouched: structured_outcome byte-identical,
	// instance still the first commit's terminal.
	if after := readStructuredOutcome(t, ctx, pool, fx.runID); string(after) != string(committed) {
		t.Errorf("structured_outcome changed across the rejected commit:\n first = %s\n after = %s", committed, after)
	}
	var (
		status     string
		exitReason *string
	)
	if err := pool.QueryRow(ctx,
		`SELECT status, exit_reason FROM agent_instances WHERE id = $1`, instanceID,
	).Scan(&status, &exitReason); err != nil {
		t.Fatalf("read agent_instances: %v", err)
	}
	if status != "succeeded" {
		t.Errorf("instance status = %q; want succeeded (unchanged)", status)
	}
	if exitReason == nil || *exitReason != ExitCompleted {
		t.Errorf("instance exit_reason = %v; want %q (unchanged)", exitReason, ExitCompleted)
	}
}
