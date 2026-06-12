//go:build integration

package schedule_test

// M9 T016 — golden-path integration tests, the milestone smoke tests
// (plan §"Integration, chaos, and regression test plan"):
//
//   - TestTicketModeGoldenPath: seeded due ticket-mode task → tickOnce →
//     ticket row with rendered templates + run row fired+ticket_id +
//     LISTEN-observed notify + fake-agent dispatcher spawn +
//     next_fire_at advanced exactly once (SC-001).
//   - TestOneshotGoldenPath: seeded oneshot task → tickOnce →
//     dispatcher → SpawnOneshot with a fake agent (mockclaude) emitting
//     a finalize_oneshot NDJSON fixture → structured_outcome +
//     verification on the run + terminal instance carrying
//     scheduled_task_run_id + zero tickets rows (SC-002 / FR-300).
//
// External test package (schedule_test), not package schedule: the
// suite dispatches into internal/spawn, and spawn imports schedule
// (oneshot.go), so an in-package test importing spawn is an import
// cycle the Go toolchain rejects. tickOnce reaches the suite through
// schedule.TickOnce (integration_test.go bridge); the dispatcher
// handlers mirror cmd/supervisor's buildDispatcherWithExtras closures
// exactly.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/dockerexec"
	"github.com/garrison-hq/garrison/supervisor/internal/events"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/schedule"
	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// goldenTicketChannel is the channel the tickets INSERT trigger emits
// for the engineering todo column (M2.1 emit_ticket_created) — the
// existing notify shape ticket-mode firings reuse unchanged (FR-200).
const goldenTicketChannel = "work.ticket.created.engineering.todo"

const (
	goldenObjectiveTemplate  = "Summarize activity since {{last_fired_at}}."
	goldenAcceptanceTemplate = "Digest posted for the slot at {{fire_at}}."
)

// goldenNow returns a microsecond-truncated UTC wall time so values
// written through timestamptz columns round-trip exactly.
func goldenNow() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func goldenLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// goldenScheduleDeps builds the schedule.Deps TickOnce needs, with a
// deterministic clock.
func goldenScheduleDeps(pool *pgxpool.Pool, now time.Time) schedule.Deps {
	return schedule.Deps{
		Pool:         pool,
		Queries:      store.New(pool),
		Logger:       goldenLogger(),
		TickInterval: time.Second,
		Now:          func() time.Time { return now },
	}
}

// seedGoldenTask inserts a scheduled task row directly (validation is
// T004's authoring concern, not the tick loop's).
func seedGoldenTask(t *testing.T, pool *pgxpool.Pool, deptID pgtype.UUID, name, mode, expr string, nextFireAt time.Time) store.ScheduledTask {
	t.Helper()
	task, err := store.New(pool).InsertScheduledTask(context.Background(), store.InsertScheduledTaskParams{
		Name:                       name,
		DepartmentID:               deptID,
		RoleSlug:                   "engineer",
		Mode:                       mode,
		ScheduleExpr:               expr,
		NextFireAt:                 pgtype.Timestamptz{Time: nextFireAt, Valid: true},
		ObjectiveTemplate:          goldenObjectiveTemplate,
		AcceptanceCriteriaTemplate: goldenAcceptanceTemplate,
	})
	if err != nil {
		t.Fatalf("seedGoldenTask: %v", err)
	}
	return task
}

// goldenRun is the scheduled_task_runs projection the assertions read.
type goldenRun struct {
	ID                pgtype.UUID
	SlotAt            pgtype.Timestamptz
	Outcome           string
	TicketID          pgtype.UUID
	AgentInstanceID   pgtype.UUID
	StructuredOutcome []byte
}

func readGoldenRuns(t *testing.T, pool *pgxpool.Pool, taskID pgtype.UUID) []goldenRun {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id, slot_at, outcome, ticket_id, agent_instance_id, structured_outcome
		  FROM scheduled_task_runs
		 WHERE scheduled_task_id = $1
		 ORDER BY fired_at`, taskID)
	if err != nil {
		t.Fatalf("readGoldenRuns: %v", err)
	}
	defer rows.Close()
	var runs []goldenRun
	for rows.Next() {
		var r goldenRun
		if err := rows.Scan(&r.ID, &r.SlotAt, &r.Outcome, &r.TicketID, &r.AgentInstanceID, &r.StructuredOutcome); err != nil {
			t.Fatalf("readGoldenRuns scan: %v", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("readGoldenRuns rows: %v", err)
	}
	return runs
}

// listenOn opens a dedicated pgx connection (outside the pool, so the
// LISTEN never leaks into pooled conns) and subscribes to channel. The
// dotted channel names must be double-quoted (M6 retro gotcha 3).
func listenOn(t *testing.T, pool *pgxpool.Pool, channel string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("listenOn connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, fmt.Sprintf(`LISTEN %q`, channel)); err != nil {
		t.Fatalf("listenOn LISTEN %q: %v", channel, err)
	}
	return conn
}

// waitNotification blocks until one notification arrives on conn.
func waitNotification(t *testing.T, conn *pgx.Conn, timeout time.Duration) *pgconn.Notification {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("waitNotification: %v", err)
	}
	return n
}

// notifyEnvelope is the pg_notify payload shape every dispatcher
// channel shares (`{"event_id": ...}` plus channel-specific extras).
type notifyEnvelope struct {
	EventID            string `json:"event_id"`
	ScheduledTaskRunID string `json:"scheduled_task_run_id"`
}

func decodeNotify(t *testing.T, payload string) notifyEnvelope {
	t.Helper()
	var env notifyEnvelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		t.Fatalf("decodeNotify %q: %v", payload, err)
	}
	if env.EventID == "" {
		t.Fatalf("notify payload %q missing event_id", payload)
	}
	return env
}

func goldenCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("goldenCount %q: %v", query, err)
	}
	return n
}

func uuidText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		u.Bytes[0:4], u.Bytes[4:6], u.Bytes[6:8], u.Bytes[8:10], u.Bytes[10:16])
}

// TestTicketModeGoldenPath — SC-001 / US1: a due ticket-mode task fires
// through tickOnce into a rendered ticket, the run record anchors the
// ticket, the in-tx notify reaches a LISTEN subscriber, the dispatcher
// routes the event to a fake-agent Spawn, and next_fire_at advances
// exactly one future slot.
func TestTicketModeGoldenPath(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	ctx := context.Background()
	now := goldenNow()

	// Subscribe BEFORE the tick: the notify is emitted inside the tick
	// tx (FR-200) and must be observable, not reconstructed.
	lconn := listenOn(t, pool, goldenTicketChannel)

	due := now.Add(-time.Hour)
	task := seedGoldenTask(t, pool, deptID, "golden-standup", schedule.ModeTicket, "daily@09:00", due)

	fired, skipped, deferred, err := schedule.TickOnce(ctx, goldenScheduleDeps(pool, now))
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if fired != 1 || skipped != 0 || deferred != 0 {
		t.Fatalf("TickOnce = (fired=%d, skipped=%d, deferred=%d), want (1, 0, 0)", fired, skipped, deferred)
	}

	// Run row: fired, anchored to the claimed slot and the ticket (FR-201).
	runs := readGoldenRuns(t, pool, task.ID)
	if len(runs) != 1 {
		t.Fatalf("run rows = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Outcome != schedule.OutcomeFired {
		t.Fatalf("run outcome = %q, want %q", run.Outcome, schedule.OutcomeFired)
	}
	if !run.TicketID.Valid {
		t.Fatal("fired run row has no ticket_id anchor (FR-201)")
	}
	if !run.SlotAt.Time.Equal(due) {
		t.Fatalf("run slot_at = %v, want the claimed next_fire_at %v", run.SlotAt.Time, due)
	}

	// Ticket row: rendered templates, todo column, the task's role
	// riding metadata (tickets carry no role column), scheduled origin.
	var objective, acceptance, columnSlug, origin string
	var roleSlug *string
	if err := pool.QueryRow(ctx,
		`SELECT objective, acceptance_criteria, column_slug, origin, metadata->>'role_slug' FROM tickets WHERE id = $1`,
		run.TicketID,
	).Scan(&objective, &acceptance, &columnSlug, &origin, &roleSlug); err != nil {
		t.Fatalf("read fired ticket: %v", err)
	}
	if objective != "Summarize activity since never." {
		t.Fatalf("rendered objective = %q (never-fired task must render the never literal)", objective)
	}
	if want := "Digest posted for the slot at " + now.Format(time.RFC3339) + "."; acceptance != want {
		t.Fatalf("rendered acceptance = %q, want %q", acceptance, want)
	}
	if columnSlug != "todo" {
		t.Fatalf("ticket column_slug = %q, want todo", columnSlug)
	}
	if origin != "scheduled" {
		t.Fatalf("ticket origin = %q, want scheduled", origin)
	}
	if roleSlug == nil || *roleSlug != "engineer" {
		t.Fatalf("ticket metadata role_slug = %v, want engineer", roleSlug)
	}

	// next_fire_at advanced exactly once: the single next future slot,
	// no backfill (FR-104); last_fired_at records this firing (FR-107).
	expr, err := schedule.Parse(task.ScheduleExpr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var nextFireAt, lastFiredAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT next_fire_at, last_fired_at FROM scheduled_tasks WHERE id = $1`, task.ID,
	).Scan(&nextFireAt, &lastFiredAt); err != nil {
		t.Fatalf("reread task: %v", err)
	}
	if !nextFireAt.Time.Equal(expr.Next(now)) {
		t.Fatalf("next_fire_at = %v, want exactly one future slot %v", nextFireAt.Time, expr.Next(now))
	}
	if !lastFiredAt.Valid || !lastFiredAt.Time.Equal(now) {
		t.Fatalf("last_fired_at = %v (valid=%v), want %v", lastFiredAt.Time, lastFiredAt.Valid, now)
	}

	// The in-tx notify was observed via LISTEN, carrying the event id
	// of the outbox row the trigger wrote in the same tx.
	n := waitNotification(t, lconn, 10*time.Second)
	if n.Channel != goldenTicketChannel {
		t.Fatalf("notify channel = %q, want %q", n.Channel, goldenTicketChannel)
	}
	env := decodeNotify(t, n.Payload)
	var outboxEventID pgtype.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM event_outbox WHERE channel = $1`, goldenTicketChannel,
	).Scan(&outboxEventID); err != nil {
		t.Fatalf("read ticket-created outbox row: %v", err)
	}
	if env.EventID != uuidText(outboxEventID) {
		t.Fatalf("notify event_id = %q, want the outbox row id %q", env.EventID, uuidText(outboxEventID))
	}

	// Dispatcher spawn under fake agent: the exact handler shape
	// cmd/supervisor wires for ticket-created channels.
	spawnDeps := spawn.Deps{
		Pool:              pool,
		Queries:           store.New(pool),
		Logger:            goldenLogger(),
		SubprocessTimeout: 30 * time.Second,
		FakeAgentCmd:      "/bin/echo golden-ticket-agent",
	}
	dispatcher := events.NewDispatcher(map[string]events.Handler{
		goldenTicketChannel: func(hctx context.Context, eventID pgtype.UUID) error {
			return spawn.Spawn(hctx, spawnDeps, eventID, "engineer")
		},
	})
	if err := dispatcher.Dispatch(ctx, n.Channel, n.Payload); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// The fake-agent spawn landed: one terminal instance anchored to
	// the fired ticket, and the event is processed (no re-dispatch).
	var instanceStatus string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM agent_instances WHERE ticket_id = $1`, run.TicketID,
	).Scan(&instanceStatus); err != nil {
		t.Fatalf("read agent_instances for fired ticket: %v", err)
	}
	if instanceStatus != "succeeded" {
		t.Fatalf("instance status = %q, want succeeded (fake agent exit 0)", instanceStatus)
	}
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT processed_at FROM event_outbox WHERE id = $1`, outboxEventID,
	).Scan(&processed); err != nil {
		t.Fatalf("read event_outbox processed_at: %v", err)
	}
	if !processed.Valid {
		t.Fatal("event_outbox.processed_at is NULL after the dispatcher spawn")
	}
}

// -----------------------------------------------------------------------
// Oneshot golden path — fake agent (mockclaude) + fake palace
// -----------------------------------------------------------------------

// goldenFakePalaceExec satisfies dockerexec.DockerExec with canned
// JSON-RPC responses (ids 1+2 — the AddDrawer / one-triple AddTriples
// shape), so WriteFinalizeOneshot's palace writes succeed without a
// MemPalace sidecar (the M7.1 fake-palace pattern).
type goldenFakePalaceExec struct {
	mu    sync.Mutex
	calls int
}

func (f *goldenFakePalaceExec) Run(_ context.Context, _ []string, stdin io.Reader) ([]byte, []byte, error) {
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return []byte(`{"jsonrpc":"2.0","id":1,"result":{}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"result":{}}` + "\n"), nil, nil
}

func (f *goldenFakePalaceExec) RunStream(
	_ context.Context,
	_ []string,
	_ func(stdin io.WriteCloser) error,
	_ func(stdout io.Reader) error,
) (*exec.Cmd, error) {
	return nil, errors.New("goldenFakePalaceExec: RunStream not implemented")
}

func (f *goldenFakePalaceExec) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// interface guard — dockerexec.DockerExec is what mempalace.Client.Exec expects.
var _ dockerexec.DockerExec = (*goldenFakePalaceExec)(nil)

// buildMockClaude compiles the repo's mockclaude drop-in (the M2.1
// fake-claude harness) into a temp dir and returns the binary path.
func buildMockClaude(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "mockclaude")
	cmd := exec.Command("go", "build", "-o", out,
		"github.com/garrison-hq/garrison/supervisor/internal/spawn/mockclaude")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build mockclaude: %v\n%s", err, output)
	}
	return out
}

const goldenOneshotOutcome = "Scheduled digest compiled and recorded for the engineering department"

// goldenOneshotRationale is ≥ the 200-char thin-diary threshold so the
// verification sub-object records thin_diary=false.
const goldenOneshotRationale = "Scanned the engineering department's activity since the previous firing and compiled the digest for the operator. " +
	"Covered ticket movement, notable diary entries, and the knowledge-graph facts recorded during the window under review."

// writeOneshotFixture writes the finalize_oneshot NDJSON stream
// mockclaude replays: healthy init frame, one finalize_oneshot
// tool_use/tool_result pair (payload satisfies every ValidateOneshot
// constraint, NO ticket_id — FR-301), then the terminal result frame.
func writeOneshotFixture(t *testing.T) string {
	t.Helper()
	input := map[string]any{
		"outcome": goldenOneshotOutcome,
		"diary_entry": map[string]any{
			"rationale":   goldenOneshotRationale,
			"artifacts":   []string{"digest.md"},
			"blockers":    []string{},
			"discoveries": []string{"activity clusters early in the week"},
		},
		"kg_triples": []map[string]string{
			{"subject": "scheduled-digest", "predicate": "covers", "object": "engineering", "valid_from": "now"},
		},
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal finalize_oneshot input: %v", err)
	}

	initFrame := `{"type":"system","subtype":"init","session_id":"sess-m9-golden","cwd":"/workspace","mcp_servers":[{"name":"postgres","status":"connected"},{"name":"finalize","status":"connected"},{"name":"garrison-mutate","status":"connected"}]}`
	assistant := fmt.Sprintf(
		`{"type":"assistant","message":{"model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_m9_oneshot","name":"mcp__finalize__finalize_oneshot","input":%s}]}}`,
		inputJSON,
	)
	toolResult := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_m9_oneshot","is_error":false,"content":[{"type":"text","text":"{\"ok\":true,\"attempt\":1}"}]}]}}`
	result := `{"type":"result","subtype":"success","is_error":false,"duration_ms":700,"total_cost_usd":0.012,"stop_reason":"end_turn","session_id":"sess-m9-golden"}`

	path := filepath.Join(t.TempDir(), "m9_oneshot_golden.ndjson")
	stream := initFrame + "\n" + assistant + "\n" + toolResult + "\n" + result + "\n"
	if err := os.WriteFile(path, []byte(stream), 0o600); err != nil {
		t.Fatalf("write oneshot fixture: %v", err)
	}
	return path
}

// goldenStructuredOutcome mirrors the persisted structured_outcome
// JSONB shape (payload + verification sub-object) for assertions.
type goldenStructuredOutcome struct {
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

// TestOneshotGoldenPath — SC-002 / US3: a due oneshot task fires
// through tickOnce into a notify the dispatcher routes to SpawnOneshot;
// the (fake) agent emits a finalize_oneshot NDJSON fixture through the
// real direct-exec pipeline; the atomic commit lands structured_outcome
// + verification on the run and the terminal instance carries the
// scheduled_task_run_id origin; no ticket row exists at any point
// (FR-300).
func TestOneshotGoldenPath(t *testing.T) {
	pool := testdb.Start(t)
	deptID := testdb.SeedM21(t, t.TempDir())
	ctx := context.Background()
	now := goldenNow()

	mockClaude := buildMockClaude(t)
	t.Setenv("GARRISON_MOCK_CLAUDE_SCRIPT", writeOneshotFixture(t))

	lconn := listenOn(t, pool, schedule.ChannelOneshotDue)

	due := now.Add(-time.Minute)
	task := seedGoldenTask(t, pool, deptID, "golden-probe", schedule.ModeOneshot, "every@30m", due)

	fired, skipped, deferred, err := schedule.TickOnce(ctx, goldenScheduleDeps(pool, now))
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if fired != 1 || skipped != 0 || deferred != 0 {
		t.Fatalf("TickOnce = (fired=%d, skipped=%d, deferred=%d), want (1, 0, 0)", fired, skipped, deferred)
	}

	runs := readGoldenRuns(t, pool, task.ID)
	if len(runs) != 1 {
		t.Fatalf("run rows = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.Outcome != schedule.OutcomeFired {
		t.Fatalf("run outcome = %q, want %q", run.Outcome, schedule.OutcomeFired)
	}
	if run.TicketID.Valid {
		t.Fatal("oneshot run row carries a ticket_id, want NULL (FR-300)")
	}

	// The oneshot notify was observed via LISTEN and names this run.
	n := waitNotification(t, lconn, 10*time.Second)
	if n.Channel != schedule.ChannelOneshotDue {
		t.Fatalf("notify channel = %q, want %q", n.Channel, schedule.ChannelOneshotDue)
	}
	env := decodeNotify(t, n.Payload)
	if env.ScheduledTaskRunID != uuidText(run.ID) {
		t.Fatalf("notify scheduled_task_run_id = %q, want %q", env.ScheduledTaskRunID, uuidText(run.ID))
	}

	// Dispatcher → SpawnOneshot over the real direct-exec pipeline with
	// mockclaude as the agent — the handler shape main.go registers for
	// work.scheduled.oneshot_due.
	cache, err := agents.NewCache(ctx, store.New(pool))
	if err != nil {
		t.Fatalf("agents.NewCache: %v", err)
	}
	palaceExec := &goldenFakePalaceExec{}
	spawnDeps := spawn.Deps{
		Pool:              pool,
		Queries:           store.New(pool),
		Logger:            goldenLogger(),
		SubprocessTimeout: 60 * time.Second,
		UseDirectExec:     true,
		ClaudeBin:         mockClaude,
		ClaudeModel:       "claude-sonnet-4-5",
		ClaudeBudgetUSD:   0.10,
		MCPConfigDir:      t.TempDir(),
		SupervisorBin:     "/usr/local/bin/garrison-supervisor",
		AgentRODSN:        "postgres://garrison_agent_ro:pw@garrison-postgres:5432/garrison",
		DatabaseURL:       "postgres://garrison:pw@garrison-postgres:5432/garrison",
		AgentsCache:       cache,
		Palace: &mempalace.Client{
			DockerBin:          "/usr/bin/docker",
			MempalaceContainer: "garrison-mempalace",
			PalacePath:         "/palace",
			Timeout:            5 * time.Second,
			Exec:               palaceExec,
		},
		FinalizeWriteTimeout: 10 * time.Second,
	}
	dispatcher := events.NewDispatcher(map[string]events.Handler{
		schedule.ChannelOneshotDue: func(hctx context.Context, eventID pgtype.UUID) error {
			return spawn.SpawnOneshot(hctx, spawnDeps, eventID)
		},
	})
	if err := dispatcher.Dispatch(ctx, n.Channel, n.Payload); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// structured_outcome + verification landed on the run (FR-403's
	// inline predicates), and the run is anchored to the instance.
	runs = readGoldenRuns(t, pool, task.ID)
	if len(runs) != 1 {
		t.Fatalf("run rows after dispatch = %d, want 1", len(runs))
	}
	run = runs[0]
	if len(run.StructuredOutcome) == 0 {
		t.Fatal("structured_outcome is NULL; want the finalize_oneshot commit document")
	}
	var doc goldenStructuredOutcome
	if err := json.Unmarshal(run.StructuredOutcome, &doc); err != nil {
		t.Fatalf("decode structured_outcome: %v", err)
	}
	if doc.Outcome != goldenOneshotOutcome {
		t.Fatalf("structured_outcome.outcome = %q, want %q", doc.Outcome, goldenOneshotOutcome)
	}
	if doc.DiaryEntry.Rationale != goldenOneshotRationale {
		t.Fatalf("structured_outcome.diary_entry.rationale = %q, want the fixture rationale", doc.DiaryEntry.Rationale)
	}
	if len(doc.KGTriples) != 1 || doc.KGTriples[0].Subject != "scheduled-digest" {
		t.Fatalf("structured_outcome.kg_triples = %+v, want the one fixture triple", doc.KGTriples)
	}
	// diary_length measures the serialized drawer body (the same
	// surface the M2.x thin-diary predicate evaluates), which embeds
	// the rationale — so it is at least that long.
	v := doc.Verification
	if v.DiaryLength < len(goldenOneshotRationale) {
		t.Fatalf("verification.diary_length = %d, want ≥ %d (the embedded rationale)", v.DiaryLength, len(goldenOneshotRationale))
	}
	if v.ThinDiaryThreshold != 200 {
		t.Fatalf("verification.thin_diary_threshold = %d, want 200", v.ThinDiaryThreshold)
	}
	if v.ThinDiary {
		t.Fatalf("verification.thin_diary = true, want false (diary_length %d ≥ threshold %d)", v.DiaryLength, v.ThinDiaryThreshold)
	}
	if v.KGTripleCount != 1 || v.MissingKGFacts {
		t.Fatalf("verification = (kg_triple_count=%d, missing_kg_facts=%v), want (1, false)", v.KGTripleCount, v.MissingKGFacts)
	}

	// Terminal instance: succeeded via the finalize commit, carrying
	// the scheduled_task_run_id origin and no ticket anchor.
	if !run.AgentInstanceID.Valid {
		t.Fatal("run.agent_instance_id is NULL; want the spawned instance anchor")
	}
	var (
		instanceStatus string
		exitReason     *string
		ticketID       pgtype.UUID
		originRunID    pgtype.UUID
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, exit_reason, ticket_id, scheduled_task_run_id
		  FROM agent_instances WHERE id = $1`, run.AgentInstanceID,
	).Scan(&instanceStatus, &exitReason, &ticketID, &originRunID); err != nil {
		t.Fatalf("read agent_instances: %v", err)
	}
	if instanceStatus != "succeeded" {
		t.Fatalf("instance status = %q, want succeeded", instanceStatus)
	}
	if exitReason == nil || *exitReason != spawn.ExitCompleted {
		t.Fatalf("instance exit_reason = %v, want %q", exitReason, spawn.ExitCompleted)
	}
	if ticketID.Valid {
		t.Fatalf("instance ticket_id = %s, want NULL for the oneshot origin", uuidText(ticketID))
	}
	if uuidText(originRunID) != uuidText(run.ID) {
		t.Fatalf("instance scheduled_task_run_id = %s, want %s", uuidText(originRunID), uuidText(run.ID))
	}

	// Zero tickets rows — the firing's whole lifecycle never touched
	// Kanban machinery (FR-300).
	if tickets := goldenCount(t, pool, `SELECT COUNT(*) FROM tickets`); tickets != 0 {
		t.Fatalf("tickets rows = %d, want 0 (FR-300)", tickets)
	}

	// next_fire_at advanced exactly one future slot; the palace writes
	// (AddDrawer + AddTriples) fired; the event is processed inside the
	// finalize tx so the poll fallback cannot re-dispatch.
	expr, err := schedule.Parse(task.ScheduleExpr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var nextFireAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`SELECT next_fire_at FROM scheduled_tasks WHERE id = $1`, task.ID,
	).Scan(&nextFireAt); err != nil {
		t.Fatalf("reread task: %v", err)
	}
	if !nextFireAt.Time.Equal(expr.Next(now)) {
		t.Fatalf("next_fire_at = %v, want exactly one future slot %v", nextFireAt.Time, expr.Next(now))
	}
	if got := palaceExec.callCount(); got != 2 {
		t.Fatalf("palace exec calls = %d, want 2 (AddDrawer + AddTriples)", got)
	}
	var processed pgtype.Timestamptz
	if err := pool.QueryRow(ctx, `
		SELECT processed_at FROM event_outbox WHERE channel = $1`, schedule.ChannelOneshotDue,
	).Scan(&processed); err != nil {
		t.Fatalf("read oneshot event_outbox row: %v", err)
	}
	if !processed.Valid {
		t.Fatal("oneshot event_outbox.processed_at is NULL; want marked inside the finalize tx")
	}
}
