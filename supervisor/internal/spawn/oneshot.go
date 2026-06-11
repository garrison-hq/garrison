package spawn

// oneshot.go is the M9 oneshot spawn path (plan §2 / specs/021-m9-
// scheduled-wakeups): SpawnOneshot consumes a work.scheduled.oneshot_due
// event and drives the target agent through the existing claude pipeline
// with the scheduled run record as origin — no ticket row, no Kanban
// machinery at any point in the firing's lifecycle (FR-300). The
// pipeline's onCommit routes to WriteFinalizeOneshot (T008): one tx
// committing UpdateRunStructuredOutcome (payload + verification
// sub-object) + the palace diary/KG writes + the terminal
// agent_instances row, with SelectScheduledTaskRunFinalizedState as the
// double-commit guard (FR-260 analog).

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/agentcontainer"
	"github.com/garrison-hq/garrison/supervisor/internal/agentpolicy"
	"github.com/garrison-hq/garrison/supervisor/internal/agents"
	"github.com/garrison-hq/garrison/supervisor/internal/concurrency"
	"github.com/garrison-hq/garrison/supervisor/internal/finalize"
	"github.com/garrison-hq/garrison/supervisor/internal/mcpconfig"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/schedule"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// oneshotEventPayload is the work.scheduled.oneshot_due outbox-row
// envelope written by schedule.fireOneshotMode. event_id rides only the
// notify body (the row id IS the event id); the row payload carries the
// three fields below.
type oneshotEventPayload struct {
	ScheduledTaskRunID string `json:"scheduled_task_run_id"`
	RoleSlug           string `json:"role_slug"`
	DepartmentID       string `json:"department_id"`
}

// insertRunningOneshotInstanceSQL is the InsertRunningInstance variant
// with scheduled_task_run_id instead of ticket_id (plan §2 step 3 —
// the agent_instances_exactly_one_origin CHECK admits exactly one of
// the two anchors). Raw SQL because the M9 sqlc query set is sealed
// (plan §sqlc table) and fire.go's outbox insert sets the precedent
// for tx-level raw queries in this milestone.
const insertRunningOneshotInstanceSQL = `INSERT INTO agent_instances (department_id, scheduled_task_run_id, status, role_slug)
VALUES ($1, $2, 'running', $3)
RETURNING id`

// selectPreviousFiringSQL resolves {{last_fired_at}} for the brief: the
// previous *firing* of this task, excluding the run being spawned.
// Needed because by spawn time the tick tx's AdvanceScheduledTask has
// already overwritten scheduled_tasks.last_fired_at with THIS firing's
// timestamp — FR-107 binds {{last_fired_at}} to the previous firing,
// rendering "never" when there was none.
const selectPreviousFiringSQL = `SELECT r.fired_at
  FROM scheduled_task_runs r
 WHERE r.scheduled_task_id = $1
   AND r.outcome = 'fired'
   AND r.id <> $2
 ORDER BY r.fired_at DESC
 LIMIT 1`

// oneshotPrep is the result of SpawnOneshot's first short transaction
// (mirror of spawnPrep). done=true means there is nothing to run:
// already-processed dedupe, missing run/department mark-and-skip, or
// agent-slot defer.
type oneshotPrep struct {
	done       bool
	instanceID pgtype.UUID
	runID      pgtype.UUID
	row        store.SelectScheduledTaskByRunIDRow
	dept       store.Department

	// prevFiring is the previous firing's timestamp ({{last_fired_at}}
	// per FR-107); invalid when the task had never fired before this run.
	prevFiring pgtype.Timestamptz

	// release frees the per-agent in-flight slot (M7.1 FR-017), exactly
	// like spawnPrep.release. nil when enforcement was inert.
	release func()
}

// SpawnOneshot handles one work.scheduled.oneshot_due event end-to-end
// (plan §2 steps 1–4). Idempotent via the LockEventForProcessing dedupe
// check; a throttle/cap deferral leaves the event unprocessed
// (processed_at NULL) and writes the run's gate_deferred outcome so the
// poll fallback re-checks after the gate window (FR-401 — gate_deferred
// is NON-terminal for oneshot runs: a later successful re-dispatch
// clears the run back to fired before the instance insert).
func SpawnOneshot(ctx context.Context, deps Deps, eventID pgtype.UUID) error {
	prep, err := prepareOneshot(ctx, deps, eventID)
	if errors.Is(err, ErrSpawnDeferred) {
		return nil
	}
	if err != nil || prep.done {
		return err
	}
	if prep.release != nil {
		defer prep.release()
	}
	if deps.UseFake() {
		// M1-harness parity (mirrors Spawn's UseFake branch): integration
		// tests drive the prep contract through the fake agent. The
		// $TICKET_ID substitution slot carries the run id — oneshot
		// spawns have no ticket.
		return runFakeAgent(ctx, deps, prep.instanceID, eventID, pgtype.UUID{}, spawnPayload{
			TicketID:     formatUUID(prep.runID),
			DepartmentID: formatUUID(prep.row.DepartmentID),
		})
	}
	return runOneshotClaude(ctx, deps, eventID, prep)
}

// prepareOneshot is SpawnOneshot's first short transaction:
// LockEventForProcessing → payload decode → run/task resolution →
// concurrency.CheckCap + per-agent slot + throttle.Check →
// (clear gate_deferred → fired per FR-401) → InsertRunningOneshotInstance
// + SetRunAgentInstance → commit.
func prepareOneshot(ctx context.Context, deps Deps, eventID pgtype.UUID) (oneshotPrep, error) {
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return oneshotPrep{}, fmt.Errorf("spawn: begin oneshot dedupe tx: %w", err)
	}
	q := deps.Queries.WithTx(tx)

	evt, err := q.LockEventForProcessing(ctx, eventID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: LockEventForProcessing: %w", err)
	}
	if evt.ProcessedAt.Valid {
		_ = tx.Rollback(ctx)
		return oneshotPrep{done: true}, nil
	}

	var payload oneshotEventPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: decode oneshot payload: %w", err)
	}
	runID, err := parseUUID(payload.ScheduledTaskRunID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: scheduled_task_run_id: %w", err)
	}

	row, dept, done, err := resolveOneshotOrigin(ctx, deps, tx, q, eventID, runID)
	if err != nil || done {
		return oneshotPrep{done: done}, err
	}

	// {{last_fired_at}} lookup (FR-107): the previous firing, excluding
	// this run. ErrNoRows leaves prevFiring invalid → renders "never".
	var prevFiring pgtype.Timestamptz
	if err := tx.QueryRow(ctx, selectPreviousFiringSQL, row.ScheduledTaskID, runID).Scan(&prevFiring); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: previous-firing lookup: %w", err)
	}

	// Gate 1: department concurrency cap. Defers exactly like the
	// throttle gate below — gate_deferred run outcome, event unprocessed.
	allowed, capN, running, err := concurrency.CheckCap(ctx, q, row.DepartmentID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: CheckCap: %w", err)
	}
	if !allowed {
		detail := fmt.Sprintf("department concurrency cap reached (cap=%d, running=%d); oneshot spawn deferred", capN, running)
		return oneshotPrep{}, deferOneshot(ctx, deps, tx, q, runID, eventID, detail)
	}

	// Per-agent in-flight slot (M7.1 FR-017). Slot-busy is a plain
	// rollback-defer — the run keeps its current outcome and the next
	// poll sweep retries the still-unprocessed event.
	releaseSlot, slotFree := acquireAgentSlot(ctx, deps, dept, row.RoleSlug)
	if !slotFree {
		_ = tx.Rollback(ctx)
		deps.Logger.Info("defer: agent spawn already in flight (oneshot)",
			"event_id", formatUUID(eventID),
			"scheduled_task_run_id", formatUUID(runID),
			"role_slug", row.RoleSlug,
		)
		return oneshotPrep{done: true}, nil
	}
	slotHandedOff := false
	if releaseSlot != nil {
		defer func() {
			if !slotHandedOff {
				releaseSlot()
			}
		}()
	}

	// Gate 2: M6 company throttle at spawn-prep, exactly where reactive
	// spawns gate (FR-400). Budget defers fire the standard evidence row;
	// rate-limit pauses were audited by OnRateLimit when set (M6 T008).
	if dept.CompanyID.Valid && deps.Throttle.Pool != nil {
		decision, err := throttle.Check(ctx, deps.Throttle, q, dept.CompanyID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return oneshotPrep{}, fmt.Errorf("spawn: throttle.Check: %w", err)
		}
		if !decision.Allowed {
			if decision.Kind == throttle.KindCompanyBudgetExceeded {
				if err := throttle.FireBudgetDefer(ctx, q, dept.CompanyID,
					pgtype.Numeric{}, deps.Throttle.DefaultSpawnCostUSD, pgtype.Numeric{},
				); err != nil {
					deps.Logger.Warn("throttle: FireBudgetDefer failed; deferring without audit",
						"event_id", formatUUID(eventID),
						"company_id", formatUUID(dept.CompanyID),
						"err", err,
					)
				}
			}
			detail := fmt.Sprintf("company throttle gate deferred oneshot spawn (kind=%s)", decision.Kind)
			return oneshotPrep{}, deferOneshot(ctx, deps, tx, q, runID, eventID, detail)
		}
	}

	// FR-401: a previously deferred run clears back to fired BEFORE the
	// instance insert — gate_deferred is non-terminal for oneshot.
	if row.Outcome == schedule.OutcomeGateDeferred {
		if err := q.UpdateRunOutcome(ctx, store.UpdateRunOutcomeParams{
			ID:      runID,
			Outcome: schedule.OutcomeFired,
			Detail:  nil,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return oneshotPrep{}, fmt.Errorf("spawn: UpdateRunOutcome(fired): %w", err)
		}
	}

	var instanceID pgtype.UUID
	if err := tx.QueryRow(ctx, insertRunningOneshotInstanceSQL,
		row.DepartmentID, runID, row.RoleSlug,
	).Scan(&instanceID); err != nil {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: InsertRunningOneshotInstance: %w", err)
	}
	if err := q.SetRunAgentInstance(ctx, store.SetRunAgentInstanceParams{
		AgentInstanceID: instanceID,
		ID:              runID,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return oneshotPrep{}, fmt.Errorf("spawn: SetRunAgentInstance: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return oneshotPrep{}, fmt.Errorf("spawn: commit oneshot dedupe+running: %w", err)
	}
	slotHandedOff = true
	return oneshotPrep{
		instanceID: instanceID,
		runID:      runID,
		row:        row,
		dept:       dept,
		prevFiring: prevFiring,
		release:    releaseSlot,
	}, nil
}

// resolveOneshotOrigin resolves the run → task join and the department
// row inside the prep tx. A missing run row marks the event processed
// and skips (corrupt event posture); a missing department additionally
// writes the typed failed run outcome the Recurring jobs view surfaces
// (spec edge: "role or department deleted after task creation →
// oneshot firings fail at spawn-prep with a typed run-record outcome").
func resolveOneshotOrigin(
	ctx context.Context,
	deps Deps,
	tx pgx.Tx,
	q *store.Queries,
	eventID, runID pgtype.UUID,
) (store.SelectScheduledTaskByRunIDRow, store.Department, bool, error) {
	row, err := q.SelectScheduledTaskByRunID(ctx, runID)
	if errors.Is(err, pgx.ErrNoRows) {
		if mErr := q.MarkEventProcessed(ctx, eventID); mErr != nil {
			_ = tx.Rollback(ctx)
			return row, store.Department{}, false, fmt.Errorf("spawn: MarkEventProcessed missing-run: %w", mErr)
		}
		if cErr := tx.Commit(ctx); cErr != nil {
			return row, store.Department{}, false, fmt.Errorf("spawn: commit missing-run: %w", cErr)
		}
		deps.Logger.Error("scheduled run missing for oneshot event",
			"event_id", formatUUID(eventID),
			"scheduled_task_run_id", formatUUID(runID),
		)
		return row, store.Department{}, true, nil
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return row, store.Department{}, false, fmt.Errorf("spawn: SelectScheduledTaskByRunID: %w", err)
	}

	dept, err := q.GetDepartmentByID(ctx, row.DepartmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		detail := "department missing at oneshot spawn-prep"
		if uErr := q.UpdateRunOutcome(ctx, store.UpdateRunOutcomeParams{
			ID:      runID,
			Outcome: schedule.OutcomeFailed,
			Detail:  &detail,
		}); uErr != nil {
			_ = tx.Rollback(ctx)
			return row, store.Department{}, false, fmt.Errorf("spawn: UpdateRunOutcome missing-dept: %w", uErr)
		}
		if mErr := q.MarkEventProcessed(ctx, eventID); mErr != nil {
			_ = tx.Rollback(ctx)
			return row, store.Department{}, false, fmt.Errorf("spawn: MarkEventProcessed missing-dept: %w", mErr)
		}
		if cErr := tx.Commit(ctx); cErr != nil {
			return row, store.Department{}, false, fmt.Errorf("spawn: commit missing-dept: %w", cErr)
		}
		deps.Logger.Error("department missing for oneshot event",
			"event_id", formatUUID(eventID),
			"scheduled_task_run_id", formatUUID(runID),
			"department_id", formatUUID(row.DepartmentID),
			"reason", ExitDepartmentMissing,
		)
		return row, store.Department{}, true, nil
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return row, store.Department{}, false, fmt.Errorf("spawn: GetDepartmentByID: %w", err)
	}
	return row, dept, false, nil
}

// deferOneshot writes the gate_deferred run outcome, commits the tx,
// and returns ErrSpawnDeferred. The event_outbox row stays unprocessed
// (processed_at NULL) so the M1 poll fallback re-checks the gate after
// the pause/budget window — the M6 back-pressure posture (FR-401).
func deferOneshot(ctx context.Context, deps Deps, tx pgx.Tx, q *store.Queries, runID, eventID pgtype.UUID, detail string) error {
	if err := q.UpdateRunOutcome(ctx, store.UpdateRunOutcomeParams{
		ID:      runID,
		Outcome: schedule.OutcomeGateDeferred,
		Detail:  &detail,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("spawn: UpdateRunOutcome(gate_deferred): %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("spawn: commit oneshot gate defer: %w", err)
	}
	deps.Logger.Info("defer: oneshot gate",
		"event_id", formatUUID(eventID),
		"scheduled_task_run_id", formatUUID(runID),
		"detail", detail,
	)
	return ErrSpawnDeferred
}

// oneshotRunInputs bundles everything the transport-specific run
// functions need beyond Deps (S107 arity discipline; mirror of
// containerRunInputs for the oneshot origin).
type oneshotRunInputs struct {
	prep       oneshotPrep
	eventID    pgtype.UUID
	agent      agents.Agent
	fetched    map[string]vault.SecretValue
	argvIn     argvParams
	wakeStatus mempalace.Status
	logger     *slog.Logger
}

// runOneshotClaude mirrors runRealClaude's shared steps for the oneshot
// origin: agent resolve → M7 hashes → Rule 3 pre-check → vault →
// wake-up → brief render → transport dispatch. Pre-pipeline failures
// write UpdateRunOutcome(failed, detail) + the failed instance terminal
// (plan §2 — once the pipeline is running, the run row keeps
// outcome='fired' and the instance's terminal status is the completion
// state, decision 5).
func runOneshotClaude(ctx context.Context, deps Deps, eventID pgtype.UUID, prep oneshotPrep) error {
	logger := deps.Logger.With(
		"event_id", formatUUID(eventID),
		"instance_id", formatUUID(prep.instanceID),
		"scheduled_task_run_id", formatUUID(prep.runID),
		"scheduled_task", prep.row.Name,
		"role_slug", prep.row.RoleSlug,
	)
	writeFail := func(exitReason, detail string) error {
		return writeOneshotPrePipelineFail(ctx, deps, prep, eventID, exitReason, detail, logger)
	}

	if deps.AgentsCache == nil {
		logger.Error("agents cache not wired; cannot resolve agent")
		return writeFail(ExitAgentMissing, "agents cache not wired")
	}
	agent, err := deps.AgentsCache.GetForDepartmentAndRole(ctx, prep.dept.ID, prep.row.RoleSlug)
	if err != nil {
		logger.Error("no agent for department+role", "err", err)
		return writeFail(ExitAgentMissing, fmt.Sprintf("no agent for department+role %q: %v", prep.row.RoleSlug, err))
	}

	if errH := recordM7HashesForInstance(ctx, deps, prep.instanceID, prep.dept, agent); errH != nil {
		logger.Warn("M7 hash record best-effort failed", "err", errH)
	}

	if err := mcpconfig.CheckExtraServers(agent.McpConfig); err != nil {
		if errors.Is(err, mcpconfig.ErrVaultMCPBanned) {
			logger.Error("Rule 3 pre-check: vault-pattern server in agents.mcp_config", "err", err)
			return writeFail(ExitVaultMCPInConfig, err.Error())
		}
		logger.Error("Rule 3 pre-check: parse agents.mcp_config failed", "err", err)
		return writeFail(ExitSpawnFailed, "parse agents.mcp_config failed")
	}
	fetched, vaultExit := vaultOrchestrate(ctx, deps, prep.row.RoleSlug, prep.instanceID, pgtype.UUID{}, agent.AgentMD, logger)
	if vaultExit != "" {
		return writeFail(vaultExit, "vault step failed: "+vaultExit)
	}
	defer zeroFetched(fetched)

	wakeUpStdout, wakeUpStatus := oneshotWakeup(ctx, deps, agent, logger)

	// Brief render (FR-107 / FR-300). {{fire_at}} is this firing's
	// timestamp — the value the tick tx persisted to last_fired_at via
	// AdvanceScheduledTask(fired=true); slot_at is the defensive
	// fallback (a fired oneshot run always has last_fired_at set before
	// its notify/poll dispatch can observe it).
	fireAt := prep.row.SlotAt.Time
	if prep.row.LastFiredAt.Valid {
		fireAt = prep.row.LastFiredAt.Time
	}
	objective := schedule.RenderTemplate(prep.row.ObjectiveTemplate, fireAt, prep.prevFiring)
	acceptance := schedule.RenderTemplate(prep.row.AcceptanceCriteriaTemplate, fireAt, prep.prevFiring)

	instanceIDText := formatUUID(prep.instanceID)
	runIDText := formatUUID(prep.runID)
	argvIn := claudeArgvInputs(deps, agent, prep.row.RoleSlug, "", instanceIDText, wakeUpStdout)
	argvIn.TaskDescription = oneshotBrief(prep.row.RoleSlug, runIDText, instanceIDText, objective, acceptance)
	argvIn.SystemPrompt = oneshotSystemPrompt(agent.AgentMD, wakeUpStdout, prep.row.Name, runIDText, instanceIDText)

	in := oneshotRunInputs{
		prep:       prep,
		eventID:    eventID,
		agent:      agent,
		fetched:    fetched,
		argvIn:     argvIn,
		wakeStatus: wakeUpStatus,
		logger:     logger,
	}
	if !deps.UseDirectExec && deps.AgentContainer != nil {
		return runOneshotViaContainer(ctx, deps, in)
	}
	return runOneshotDirect(ctx, deps, in)
}

// oneshotWakeup is the M2.2 wake-up capture for oneshot spawns
// (byte-for-byte the runRealClaude step 3a behavior — non-blocking on
// every failure mode per FR-207b).
func oneshotWakeup(ctx context.Context, deps Deps, agent agents.Agent, logger *slog.Logger) (string, mempalace.Status) {
	wakeUpStatus := mempalace.StatusSkipped
	wakeUpStdout := ""
	if agent.PalaceWing != nil && *agent.PalaceWing != "" {
		wakeUpCtx, wakeUpCancel := context.WithTimeout(ctx, 2*time.Second)
		stdout, status, elapsed, werr := mempalace.Wakeup(wakeUpCtx, mempalace.WakeupConfig{
			DockerBin:          deps.DockerBin,
			MempalaceContainer: deps.MempalaceContainer,
			PalacePath:         deps.PalacePath,
			Timeout:            2 * time.Second,
			Logger:             logger,
		}, *agent.PalaceWing)
		wakeUpCancel()
		_ = werr // non-blocking: Wakeup returns nil err on every failure mode
		wakeUpStdout = stdout
		wakeUpStatus = status
		logger.Info("wake_up_complete",
			"palace_wing", *agent.PalaceWing,
			"wake_up_status", string(status),
			"elapsed_ms", elapsed.Milliseconds(),
		)
	}
	return wakeUpStdout, wakeUpStatus
}

// oneshotBrief renders the -p task prompt: the templated brief is the
// prompt source (plan §2 step 4 / FR-300) — objective + acceptance
// criteria, with the run + instance identifiers the agent needs without
// querying.
func oneshotBrief(roleSlug, runID, instanceID, objective, acceptance string) string {
	return fmt.Sprintf(
		"You are the %s on scheduled oneshot run %s (agent_instance %s).\n\n## Objective\n\n%s\n\n## Acceptance criteria\n\n%s\n\nExecute the objective, then exit through your completion protocol by calling the finalize_oneshot tool.",
		roleSlug, runID, instanceID, objective, acceptance,
	)
}

// oneshotSystemPrompt composes the oneshot system prompt: same
// preamble + agent.md + wake-up shape as mempalace.ComposeSystemPrompt,
// with a "This turn" block that names the scheduled run instead of a
// ticket (FR-304 — the brief and per-spawn config make the expected
// exit unambiguous).
func oneshotSystemPrompt(agentMD, wakeUpStdout, taskName, runID, instanceID string) string {
	thisTurn := "## This turn\n\nYou have been spawned as agent_instance " + instanceID +
		" for scheduled task " + fmt.Sprintf("%q", taskName) +
		" (oneshot run " + runID + ")." +
		" Execute the brief in your task prompt, then exit by calling the finalize_oneshot tool. There is no ticket for this run.\n"

	var body string
	if wakeUpStdout == "" {
		body = agentMD + "\n\n---\n\n" + thisTurn
	} else {
		body = agentMD + "\n\n---\n\n## Wake-up context\n\n" + wakeUpStdout +
			"\n\n---\n\n" + thisTurn
	}
	return agentpolicy.PrependPreamble(body)
}

// buildOneshotMCPConfig is the oneshot MCP-config builder: the standard
// sealed Render (Rule 3 included) plus the finalize-mode env seam
// (spawn.go injectOneshotFinalizeEnv). Both transports call it so the
// finalize entry provably carries GARRISON_FINALIZE_MODE=oneshot +
// GARRISON_SCHEDULED_RUN_ID and no ticket env, whichever path runs.
func buildOneshotMCPConfig(p mcpconfig.WriteParams, scheduledRunID string) ([]byte, string, error) {
	data, fileName, err := mcpconfig.Render(p)
	if err != nil {
		return nil, "", err
	}
	data, err = injectOneshotFinalizeEnv(data, scheduledRunID)
	if err != nil {
		return nil, "", err
	}
	return data, fileName, nil
}

// writeOneshotPrePipelineFail records a pre-pipeline oneshot failure:
// UpdateRunOutcome(failed, detail) — the typed run outcome the
// Recurring jobs view surfaces (FR-403) — plus the failed instance
// terminal + MarkEventProcessed. failed is terminal for the slot;
// the next slot fires on schedule.
func writeOneshotPrePipelineFail(
	ctx context.Context,
	deps Deps,
	prep oneshotPrep,
	eventID pgtype.UUID,
	exitReason, detail string,
	logger *slog.Logger,
) error {
	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TerminalWriteGrace)
	defer cancel()
	failDetail := detail
	if err := deps.Queries.UpdateRunOutcome(termCtx, store.UpdateRunOutcomeParams{
		ID:      prep.runID,
		Outcome: schedule.OutcomeFailed,
		Detail:  &failDetail,
	}); err != nil {
		logger.Error("UpdateRunOutcome(failed) write failed; instance terminal still records the failure", "err", err)
	}
	return writeTerminalCostAndWakeup(termCtx, deps, terminalWriteParams{
		InstanceID: prep.instanceID,
		EventID:    eventID,
		Status:     "failed",
		ExitReason: exitReason,
	})
}

// -----------------------------------------------------------------------
// Transport-specific run paths (mirrors of runRealClaude's tail and
// runRealClaudeViaContainer, oneshot origin — no ticket, no transition)
// -----------------------------------------------------------------------

// runOneshotDirect is the direct-exec transport tail for oneshot spawns
// (soak-window rollback lever parity with ticket spawns; production at
// M9 runs the container path below).
func runOneshotDirect(ctx context.Context, deps Deps, in oneshotRunInputs) error {
	logger := in.logger.With("via", "direct_exec")
	writeFail := func(exitReason, detail string) error {
		return writeOneshotPrePipelineFail(ctx, deps, in.prep, in.eventID, exitReason, detail, logger)
	}

	data, fileName, err := buildOneshotMCPConfig(mcpconfig.WriteParams{
		InstanceID:    in.prep.instanceID,
		SupervisorBin: deps.SupervisorBin,
		DSN:           deps.AgentRODSN,
		Mempalace: mcpconfig.MempalaceParams{
			DockerBin:          deps.DockerBin,
			MempalaceContainer: deps.MempalaceContainer,
			PalacePath:         deps.PalacePath,
			DockerHost:         deps.DockerHost,
		},
		Finalize: mcpconfig.FinalizeParams{
			SupervisorBin:   deps.SupervisorBin,
			AgentInstanceID: formatUUID(in.prep.instanceID),
			DatabaseURL:     deps.AgentRODSN,
		},
		GarrisonMutate: mcpconfig.GarrisonMutateParams{
			SupervisorBin:   deps.SupervisorBin,
			AgentInstanceID: formatUUID(in.prep.instanceID),
			DatabaseURL:     deps.DatabaseURL,
		},
		ExtraServersJSON: in.agent.McpConfig,
	}, formatUUID(in.prep.runID))
	if err != nil {
		if errors.Is(err, mcpconfig.ErrVaultMCPBanned) {
			logger.Error("oneshot mcpconfig: Rule 3 violation — vault-pattern MCP server", "err", err)
			return writeFail(ExitVaultMCPInConfig, err.Error())
		}
		logger.Error("oneshot mcpconfig build failed; recording spawn_failed", "err", err)
		return writeFail(ExitSpawnFailed, "MCP config build failed")
	}
	if deps.MCPConfigDir == "" {
		logger.Error("MCPConfigDir is empty; recording spawn_failed")
		return writeFail(ExitSpawnFailed, "MCPConfigDir is empty")
	}
	mcpPath := filepath.Join(deps.MCPConfigDir, fileName)
	if err := os.WriteFile(mcpPath, data, 0o600); err != nil {
		logger.Error("oneshot MCP config write failed; recording spawn_failed", "err", err)
		return writeFail(ExitSpawnFailed, "MCP config write failed")
	}
	defer func() {
		if rmErr := mcpconfig.Remove(mcpPath); rmErr != nil {
			logger.Warn("mcpconfig.Remove failed; continuing", "path", mcpPath, "err", rmErr)
		}
	}()

	execCtx, execCancel := context.WithTimeout(context.WithoutCancel(ctx), deps.SubprocessTimeout)
	defer execCancel()

	argvIn := in.argvIn
	argvIn.MCPConfigPath = mcpPath
	argv := buildClaudeArgv(argvIn)

	cmd := exec.CommandContext(execCtx, deps.ClaudeBin, argv...)
	if in.prep.dept.WorkspacePath != nil && *in.prep.dept.WorkspacePath != "" {
		if err := os.MkdirAll(*in.prep.dept.WorkspacePath, 0o755); err != nil {
			logger.Error("workspace MkdirAll failed; recording spawn_failed",
				"workspace_path", *in.prep.dept.WorkspacePath, "err", err)
			return writeFail(ExitSpawnFailed, "workspace MkdirAll failed")
		}
		cmd.Dir = *in.prep.dept.WorkspacePath
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd, syscall.SIGTERM) }
	cmd.WaitDelay = ShutdownSignalGrace
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		logger.Error("open /dev/null failed", "err", err)
		return writeFail(ExitSpawnFailed, "open /dev/null failed")
	}
	defer stdin.Close()
	cmd.Stdin = stdin
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return writeFail(ExitSpawnFailed, "stdout pipe failed")
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return writeFail(ExitSpawnFailed, "stderr pipe failed")
	}
	if len(in.fetched) > 0 {
		cmd.Env = appendSecretEnv(os.Environ(), in.fetched)
	}

	if err := cmd.Start(); err != nil {
		logger.Error("claude cmd.Start failed; recording spawn_failed", "err", err)
		return writeFail(ExitSpawnFailed, "claude cmd.Start failed")
	}
	logger = logger.With("pid", cmd.Process.Pid, "session_prep_model", argvIn.Model)
	logger.Info("oneshot claude subprocess started")

	if err := deps.Queries.UpdatePID(ctx, store.UpdatePIDParams{
		ID:  in.prep.instanceID,
		Pid: int32Ptr(int32(cmd.Process.Pid)),
	}); err != nil {
		logger.Warn("UpdatePID failed; continuing without backfill", "err", err)
	}

	var bailed atomic.Bool
	onBail := func(_ string) {
		if !bailed.CompareAndSwap(false, true) {
			return
		}
		if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
			logger.Warn("killProcessGroup on bail returned error", "err", err)
		}
	}

	directTransport := transport{
		Stdout: stdout,
		Stderr: stderr,
		Terminate: func(escalate bool) error {
			if escalate {
				return killProcessGroup(cmd, syscall.SIGKILL)
			}
			return killProcessGroup(cmd, syscall.SIGTERM)
		},
		ExitDetail: func(context.Context) WaitDetail {
			_ = cmd.Wait()
			exitCode, sigName := extractExit(cmd.ProcessState)
			wait := WaitDetail{ExitCode: exitCode}
			if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
				wait.ContextErr = context.DeadlineExceeded
			}
			if sigName != "" {
				wait.Signaled = true
				wait.Signal = signalFromName(sigName)
			}
			return wait
		},
	}
	sessIn := in
	sessIn.logger = logger
	return runOneshotSession(ctx, execCtx, deps, directTransport, sessIn, &bailed, onBail)
}

// runOneshotViaContainer is the container-exec transport tail for
// oneshot spawns — the M7.1 path (FR-008 agent-ID-keyed container,
// FR-013 shared invocation contract) with the oneshot MCP-config
// builder swapped in. Container/exec setup failures are pre-pipeline:
// they land the typed failed run outcome (plan §2 — failed is reserved
// for container missing / exec setup error).
func runOneshotViaContainer(ctx context.Context, deps Deps, in oneshotRunInputs) error {
	logger := in.logger.With("via", "agent_container")
	writeFail := func(exitReason, detail string) error {
		return writeOneshotPrePipelineFail(ctx, deps, in.prep, in.eventID, exitReason, detail, logger)
	}
	if deps.AgentContainer == nil {
		logger.Error("agent container controller not wired", "err", ErrContainerControllerMissing)
		return writeFail(ExitSpawnFailed, ErrContainerControllerMissing.Error())
	}
	agentID := formatUUID(in.agent.ID)
	name := agentcontainer.ContainerName(agentID)
	instanceIDText := formatUUID(in.prep.instanceID)
	logger = logger.With("container", name)

	cfgBytes, _, err := buildOneshotMCPConfig(mcpconfig.WriteParams{
		InstanceID:    in.prep.instanceID,
		SupervisorBin: containerSupervisorBin,
		DSN:           deps.AgentRODSN,
		Finalize: mcpconfig.FinalizeParams{
			SupervisorBin:   containerSupervisorBin,
			AgentInstanceID: instanceIDText,
			DatabaseURL:     deps.AgentRODSN,
		},
		GarrisonMutate: mcpconfig.GarrisonMutateParams{
			SupervisorBin:   containerSupervisorBin,
			AgentInstanceID: instanceIDText,
			DatabaseURL:     deps.DatabaseURL,
		},
		ExtraServersJSON: in.agent.McpConfig,
		OmitMempalace:    true,
	}, formatUUID(in.prep.runID))
	if err != nil {
		if errors.Is(err, mcpconfig.ErrVaultMCPBanned) {
			logger.Error("oneshot mcpconfig: Rule 3 violation — vault-pattern MCP server", "err", err)
			return writeFail(ExitVaultMCPInConfig, err.Error())
		}
		logger.Error("oneshot mcpconfig build failed; recording spawn_failed", "err", err)
		return writeFail(ExitSpawnFailed, "MCP config build failed")
	}
	mcpPath := containerMCPDir + "/mcp-" + instanceIDText + ".json"

	if ok, _ := writeContainerMCPConfig(ctx, deps, name, mcpPath, cfgBytes, logger); !ok {
		return writeFail(ExitSpawnFailed, "container MCP-config write exec failed")
	}
	defer cleanupContainerMCPConfig(ctx, deps, name, mcpPath, logger)

	argvIn := in.argvIn
	argvIn.MCPConfigPath = mcpPath
	argv := buildClaudeArgv(argvIn)
	cmd := make([]string, 0, len(argv)+5)
	cmd = append(cmd,
		"/usr/bin/timeout",
		"--signal=TERM",
		fmt.Sprintf("--kill-after=%ds", int(timeoutKillGrace/time.Second)),
		fmt.Sprintf("%d", int(deps.SubprocessTimeout/time.Second)),
		containerClaudeBin,
	)
	cmd = append(cmd, argv...)

	env := containerExecEnv(deps, in.fetched)

	execCtx, execCancel := context.WithTimeout(context.WithoutCancel(ctx), deps.SubprocessTimeout+containerCtxSlack)
	defer execCancel()

	sess, err := deps.AgentContainer.Exec(execCtx, name, agentcontainer.ExecSpec{
		Cmd:        cmd,
		Env:        env,
		WorkingDir: "/workspace",
	})
	if err != nil {
		logger.Error("oneshot claude exec-create failed; recording failed run", "err", err)
		return writeFail(ExitSpawnFailed, fmt.Sprintf("claude exec-create failed: %v", err))
	}
	defer func() {
		_ = sess.Stdout.Close()
		_ = sess.Stderr.Close()
	}()
	logger = logger.With("exec_id", sess.ID)
	logger.Info("oneshot claude exec started in agent container")

	restart := func(reason string) {
		rCtx, rCancel := context.WithTimeout(context.WithoutCancel(ctx), helperExecTimeout)
		defer rCancel()
		if rErr := deps.AgentContainer.Restart(rCtx, name); rErr != nil {
			logger.Warn("container restart returned error", "reason", reason, "err", rErr)
		}
	}
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go watchExecDeadline(execCtx, sessionDone, restart, logger)

	var bailed atomic.Bool
	onBail := func(reason string) {
		if !bailed.CompareAndSwap(false, true) {
			return
		}
		restart("bail:" + reason)
	}

	var wrapperFailed bool
	containerTransport := transport{
		Stdout: newDrainAheadReader(sess.Stdout),
		Stderr: newDrainAheadReader(sess.Stderr),
		Terminate: func(bool) error {
			rCtx, rCancel := context.WithTimeout(context.WithoutCancel(ctx), helperExecTimeout)
			defer rCancel()
			return deps.AgentContainer.Restart(rCtx, name)
		},
		ExitDetail: func(c context.Context) WaitDetail {
			wait, wf := inspectContainerExit(c, sess, execCtx, logger)
			wrapperFailed = wf
			return wait
		},
	}

	sessDeps := deps
	sessDeps.terminalReasonOverride = func(status, exitReason string) (string, string) {
		if wrapperFailed && exitReason == ExitNoResult {
			logger.Warn("timeout wrapper failed inside the container; classifying spawn_failed")
			return "failed", ExitSpawnFailed
		}
		return status, exitReason
	}

	sessIn := in
	sessIn.logger = logger
	return runOneshotSession(ctx, execCtx, sessDeps, containerTransport, sessIn, &bailed, onBail)
}

// runOneshotSession is the oneshot mirror of runClaudeSession: the
// same pipeline goroutine + stderr mirror + shutdown ladder + exit
// detail + Adjudicate + terminal write, minus everything ticket-shaped
// — no hello.txt acceptance gate (finalize is the exit contract,
// FR-301), no ticket transition, and finalize is ALWAYS expected
// (finalizeExpectedForRole bypassed per plan §2 step 4).
func runOneshotSession(
	ctx, execCtx context.Context,
	deps Deps,
	t transport,
	in oneshotRunInputs,
	bailed *atomic.Bool,
	onBail func(reason string),
) error {
	logger := in.logger
	if execCtx == nil {
		execCtx = ctx
	}

	var (
		result      Result
		pipelineErr error
	)
	pipelineDone := make(chan struct{})
	stderrDone := make(chan struct{})

	finalizeState := &FinalizeState{Expected: true}
	onCommit := oneshotOnCommit(execCtx, deps, in, &result, logger)

	go func() {
		defer close(pipelineDone)
		result = Result{}
		policy := NewFinalizePolicy(logger, in.prep.instanceID, pgtype.UUID{}, &result, FinalizeDeps{
			Expected:            true,
			State:               finalizeState,
			OnCommit:            onCommit,
			ResultGrace:         deps.FinalizeResultGrace,
			OnRateLimitRejected: rateLimitHook(deps, sessionParams{Dept: in.prep.dept}, &result, logger),
		}, onBail)
		result, pipelineErr = Run(execCtx, t.Stdout, policy, logger)
	}()
	go func() {
		defer close(stderrDone)
		mirrorStderrLines(t.Stderr, logger)
	}()

	shutdownCtxErr, shutdownSigkilled := awaitPipelineDrain(ctx, t, pipelineDone, logger)
	<-stderrDone
	wait := t.ExitDetail(ctx)

	if shutdownSigkilled && deps.SigkillEscalations != nil {
		deps.SigkillEscalations.Add(1)
	}
	if pipelineErr != nil && !result.ParseError {
		logger.Warn("pipeline.Run returned a non-parse error", "err", pipelineErr)
	}
	wait = overlayRunnerWaitDetail(wait, shutdownCtxErr, bailed)

	// T008's WriteFinalizeOneshot writes the terminal instance row inside
	// its own atomic tx — same double-write guard as ticket mode.
	if finalizeState.Committed {
		logger.Info("finalize_oneshot already committed atomic tx; skipping terminal write",
			"scheduled_task_run_id", formatUUID(in.prep.runID),
			"instance_id", formatUUID(in.prep.instanceID),
		)
		return nil
	}

	// helloTxtOK=true: the M1 hello.txt fallback has no oneshot analog —
	// finalize adjudication (Expected=true) is the completion contract.
	status, exitReason := Adjudicate(result, wait, true, *finalizeState)

	cost, _ := parseCostToNumeric(result.TotalCostUSD)
	if !result.ResultSeen {
		cost = pgtype.Numeric{}
	}

	logger.Info("oneshot claude session terminal",
		"status", status,
		"exit_reason", exitReason,
		"total_cost_usd", result.TotalCostUSD,
		"result_seen", result.ResultSeen,
		"assistant_seen", result.AssistantSeen,
	)

	// The run row keeps outcome='fired'; the instance's terminal status
	// is the completion state readable through the run→instance join
	// (plan decision 5). No ticket, no transition columns.
	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TerminalWriteGrace)
	defer cancel()
	return writeTerminalCostAndWakeup(termCtx, deps, terminalWriteParams{
		InstanceID:   in.prep.instanceID,
		EventID:      in.eventID,
		Status:       status,
		ExitReason:   exitReason,
		Cost:         cost,
		WakeUpStatus: string(in.wakeStatus),
	})
}

// mirrorStderrLines mirrors a session's stderr stream into slog —
// shared shape with runClaudeSession's inline goroutine.
func mirrorStderrLines(r io.Reader, logger *slog.Logger) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		logger.Info(scanner.Text(), "stream", "stderr")
	}
}

// oneshotOnCommit is the pipeline OnCommit hook for oneshot spawns
// (plan §2 step 5): re-validate the raw finalize_oneshot payload (the
// ticket-mode posture — the MCP server validated before signalling
// ok=true, so a failure here is schema drift) and route to
// WriteFinalizeOneshot's atomic tx. result points at the runner's
// pipeline result variable — the ticket-mode ResultGrace contract
// (runner.go finalizeOnCommit / FinalizeDeps.ResultGrace) applies:
// OnCommit fires after the result event lands (or the grace window
// elapses), so reading TotalCostUSD here captures the spend on the
// committed terminal row. Cost stays NULL only when the grace window
// elapsed without a result event (the M6 failure-mode contract,
// review #2: oneshot spend must be visible to the company budget gate).
func oneshotOnCommit(execCtx context.Context, deps Deps, in oneshotRunInputs, result *Result, logger *slog.Logger) func(json.RawMessage) error {
	return func(rawPayload json.RawMessage) error {
		if deps.Palace == nil {
			logger.Error("finalize_oneshot onCommit: deps.Palace is nil; skipping atomic write")
			return fmt.Errorf("finalize_oneshot: no palace client wired")
		}
		parsed, verr := finalize.ValidateOneshot(rawPayload)
		if verr != nil {
			logger.Error("finalize_oneshot onCommit: re-validation failed",
				"err", verr.Error(),
				"field", verr.Field)
			return fmt.Errorf("finalize_oneshot re-validate: %w", verr)
		}
		cost, _ := parseCostToNumeric(result.TotalCostUSD)
		if !result.ResultSeen {
			cost = pgtype.Numeric{}
		}
		return WriteFinalizeOneshot(execCtx, deps, in.prep.runID, in.prep.instanceID, *parsed, OneshotTerminal{
			Cost:         cost,
			WakeUpStatus: string(in.wakeStatus),
		})
	}
}

// -----------------------------------------------------------------------
// WriteFinalizeOneshot — the supervisor-side finalize_oneshot commit
// (M9 T008, plan §2 step 5)
// -----------------------------------------------------------------------

// defaultOneshotThinDiaryThreshold is the fallback diary-length bound
// the verification sub-object applies inline (FR-403: oneshot
// completions never touch the hygiene tables — the M2.x predicates run
// at commit time and the result lands on the run record for operator
// review). The value mirrors config.ThinDiaryThreshold's default
// (GARRISON_HYGIENE_THIN_DIARY_THRESHOLD, 200). M9 review #5:
// Deps.ThinDiaryThreshold carries the operator-tuned value; the zero
// value falls back here so existing call sites and tests keep the
// documented default.
const defaultOneshotThinDiaryThreshold = 200

// thinDiaryThreshold resolves the effective bound: the configured
// Deps.ThinDiaryThreshold when positive, the documented 200 default
// otherwise.
func thinDiaryThreshold(deps Deps) int {
	if deps.ThinDiaryThreshold > 0 {
		return deps.ThinDiaryThreshold
	}
	return defaultOneshotThinDiaryThreshold
}

// markOneshotEventProcessedSQL marks the run's work.scheduled.
// oneshot_due outbox row processed inside the atomic tx — the oneshot
// analog of ticket mode's MarkEventProcessed(meta.EventID). Resolved
// through the payload's run id because WriteFinalizeOneshot's sealed
// signature carries no event id; without this write the still-
// unprocessed event would poll-redispatch a second instance for an
// already-finalized run. Raw SQL per the sealed-M9-query-set precedent
// at the top of this file.
const markOneshotEventProcessedSQL = `UPDATE event_outbox
   SET processed_at = NOW()
 WHERE channel = 'work.scheduled.oneshot_due'
   AND payload->>'scheduled_task_run_id' = $1
   AND processed_at IS NULL`

// oneshotVerification is the verification sub-object recorded inside
// scheduled_task_runs.structured_outcome: the M2.x hygiene predicates
// (thin diary, missing KG facts) applied inline at commit time
// (FR-403's no-hygiene-table-coupling).
type oneshotVerification struct {
	DiaryLength        int  `json:"diary_length"`
	ThinDiaryThreshold int  `json:"thin_diary_threshold"`
	ThinDiary          bool `json:"thin_diary"`
	KGTripleCount      int  `json:"kg_triple_count"`
	MissingKGFacts     bool `json:"missing_kg_facts"`
}

// oneshotStructuredTriple is the persisted KG-triple shape. Explicit
// because finalize.KGTriple tags ValidFrom `json:"-"` (it is
// supervisor-substituted, not wire-carried) and the run record stores
// the full payload.
type oneshotStructuredTriple struct {
	Subject   string    `json:"subject"`
	Predicate string    `json:"predicate"`
	Object    string    `json:"object"`
	ValidFrom time.Time `json:"valid_from"`
}

// oneshotStructuredOutcome is the scheduled_task_runs.structured_outcome
// JSONB shape: the full finalize_oneshot payload plus the verification
// sub-object.
type oneshotStructuredOutcome struct {
	Outcome      string                    `json:"outcome"`
	DiaryEntry   finalize.DiaryEntry       `json:"diary_entry"`
	KGTriples    []oneshotStructuredTriple `json:"kg_triples"`
	Verification oneshotVerification       `json:"verification"`
}

// oneshotWriteFailure carries one sad-path disposition through
// failOneshotWrite (mirror of writeTerminalOutcome for the oneshot
// origin — no hygiene_status: FR-403 keeps oneshot rows out of the
// hygiene vocabulary).
type oneshotWriteFailure struct {
	reason string // canonical exit_reason value
	class  string // "palace_write" | "commit" | "timeout"
	orphan bool   // palace drawer landed before the failure
	err    error
}

// OneshotTerminal carries the commit-time terminal metadata for the
// succeeded agent_instances row (review #2): Cost is the spend the
// pipeline's deferred OnCommit captured from the result event
// (pgtype.Numeric zero value writes NULL — the grace-window-elapsed
// failure mode); WakeUpStatus mirrors the ticket-mode column ("" writes
// NULL). The zero value reproduces the pre-fix write shape, which is
// what the failure-path tests pass.
type OneshotTerminal struct {
	Cost         pgtype.Numeric
	WakeUpStatus string
}

// WriteFinalizeOneshot performs the oneshot atomic write (plan §2 step
// 5): one tx committing UpdateRunStructuredOutcome (full payload + the
// verification sub-object), the palace diary/KG writes via the existing
// client path, the terminal agent_instances row (status, exit_reason,
// cost, wake_up_status), and the event-outbox processed mark.
// SelectScheduledTaskRunFinalizedState guards double commit (FR-260
// analog) — a second commit for an already-finalized run is rejected
// without touching any row.
//
// On failure, a separate terminal failed agent_instances row is written
// outside the rolled-back tx (the ticket-mode WriteFinalize posture);
// the run row keeps outcome='fired' with the terminal state readable
// through the run→instance join (decision 5 — UpdateRunOutcome(failed)
// is reserved for pre-pipeline failures).
func WriteFinalizeOneshot(parentCtx context.Context, deps Deps, runID pgtype.UUID, instanceID pgtype.UUID, payload finalize.OneshotPayload, term OneshotTerminal) error {
	start := time.Now()
	timeout := deps.FinalizeWriteTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	// WithoutCancel: supervisor SIGTERM does NOT abort an in-flight
	// commit (AGENTS.md rule 6); the timeout is the FR-261-shaped
	// wall-clock ceiling.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), timeout)
	defer cancel()

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(
		"scheduled_task_run_id", formatUUID(runID),
		"agent_instance_id", formatUUID(instanceID),
	)

	if deps.Palace == nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write",
			err: errors.New("no palace client wired"),
		}, logger)
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: classifyCtxErr(ctx, ExitFinalizePalaceWriteFailed), class: "palace_write",
			err: fmt.Errorf("begin tx: %w", err),
		}, logger)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	q := deps.Queries.WithTx(tx)

	// Double-commit guard (FR-260 analog): a non-NULL structured_outcome
	// means a previous commit landed — reject WITHOUT writing a failure
	// terminal row (the first commit's instance state is authoritative).
	state, err := q.SelectScheduledTaskRunFinalizedState(ctx, runID)
	if err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write",
			err: fmt.Errorf("SelectScheduledTaskRunFinalizedState: %w", err),
		}, logger)
	}
	if finalized, _ := state.Finalized.(bool); finalized {
		logger.Warn("finalize_oneshot double commit rejected; run already finalized")
		return fmt.Errorf("spawn: finalize_oneshot double commit rejected: run %s already has structured_outcome", formatUUID(runID))
	}

	row, err := q.SelectScheduledTaskByRunID(ctx, runID)
	if err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write",
			err: fmt.Errorf("SelectScheduledTaskByRunID: %w", err),
		}, logger)
	}

	// M2.3 pattern-scanner hook (FR-418/FR-419 posture carried over):
	// redact secret patterns from the payload fields before anything is
	// persisted. Non-blocking — redact-and-warn only. The scan reuses
	// the ticket-path helper through a TicketID-less FinalizePayload
	// wrapper so the two paths cannot drift.
	scanned := finalize.FinalizePayload{
		Outcome:    payload.Outcome,
		DiaryEntry: payload.DiaryEntry,
		KGTriples:  payload.KGTriples,
	}
	if matched := scanAndRedactPayload(&scanned); len(matched) > 0 {
		logger.Warn("pattern scanner matched secrets in finalize_oneshot payload; redacted before palace write",
			"labels", matched)
	}

	// Drawer body + inline verification. The diary length is measured on
	// the serialized drawer body — the same surface the M2.x thin-diary
	// predicate evaluates (hygiene evaluator: len(drawer.Body)).
	body := serializeOneshotDiary(row.Name, row.ObjectiveTemplate, runID, &scanned, time.Now().UTC())
	threshold := thinDiaryThreshold(deps)
	verification := oneshotVerification{
		DiaryLength:        len(body),
		ThinDiaryThreshold: threshold,
		ThinDiary:          len(body) < threshold,
		KGTripleCount:      len(scanned.KGTriples),
		MissingKGFacts:     len(scanned.KGTriples) < 1,
	}

	wing := resolveOneshotWing(ctx, deps, row, logger)
	if err := deps.Palace.AddDrawer(ctx, wing, "hall_events", body); err != nil {
		reason, class := classifyPalaceErr(ctx, err)
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: reason, class: class,
			err: fmt.Errorf("AddDrawer: %w", err),
		}, logger)
	}
	if err := deps.Palace.AddTriples(ctx, toMempalaceTriples(scanned.KGTriples)); err != nil {
		reason, class := classifyPalaceErr(ctx, err)
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: reason, class: class, orphan: true,
			err: fmt.Errorf("AddTriples: %w", err),
		}, logger)
	}

	outcomeJSON, err := json.Marshal(buildOneshotStructuredOutcome(&scanned, verification))
	if err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write", orphan: true,
			err: fmt.Errorf("marshal structured_outcome: %w", err),
		}, logger)
	}
	if err := q.UpdateRunStructuredOutcome(ctx, store.UpdateRunStructuredOutcomeParams{
		StructuredOutcome: outcomeJSON,
		ID:                runID,
	}); err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write", orphan: true,
			err: fmt.Errorf("UpdateRunStructuredOutcome: %w", err),
		}, logger)
	}

	completedReason := ExitCompleted
	var wakeUpPtr *string
	if term.WakeUpStatus != "" {
		wakeUpPtr = &term.WakeUpStatus
	}
	if err := q.UpdateInstanceTerminalWithCostAndWakeup(ctx, store.UpdateInstanceTerminalWithCostAndWakeupParams{
		ID:           instanceID,
		Status:       "succeeded",
		ExitReason:   &completedReason,
		TotalCostUsd: term.Cost,
		WakeUpStatus: wakeUpPtr,
	}); err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write", orphan: true,
			err: fmt.Errorf("UpdateInstanceTerminalWithCostAndWakeup: %w", err),
		}, logger)
	}
	if _, err := tx.Exec(ctx, markOneshotEventProcessedSQL, formatUUID(runID)); err != nil {
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: ExitFinalizePalaceWriteFailed, class: "palace_write", orphan: true,
			err: fmt.Errorf("mark oneshot event processed: %w", err),
		}, logger)
	}

	if err := tx.Commit(ctx); err != nil {
		reason := ExitFinalizeCommitFailed
		class := "commit"
		if errors.Is(err, context.DeadlineExceeded) || isCtxDeadlineExceeded(ctx) {
			reason = ExitFinalizeWriteTimeout
			class = "timeout"
		}
		return failOneshotWrite(parentCtx, deps, instanceID, oneshotWriteFailure{
			reason: reason, class: class, orphan: true,
			err: fmt.Errorf("Commit: %w", err),
		}, logger)
	}
	committed = true

	logger.Info("oneshot_atomic_write_committed",
		"triple_count", verification.KGTripleCount,
		"diary_length", verification.DiaryLength,
		"thin_diary", verification.ThinDiary,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return nil
}

// buildOneshotStructuredOutcome assembles the persisted JSONB document
// from the (post-redaction) payload + verification.
func buildOneshotStructuredOutcome(p *finalize.FinalizePayload, v oneshotVerification) oneshotStructuredOutcome {
	triples := make([]oneshotStructuredTriple, 0, len(p.KGTriples))
	for _, t := range p.KGTriples {
		triples = append(triples, oneshotStructuredTriple{
			Subject:   t.Subject,
			Predicate: t.Predicate,
			Object:    t.Object,
			ValidFrom: t.ValidFrom,
		})
	}
	return oneshotStructuredOutcome{
		Outcome:      p.Outcome,
		DiaryEntry:   p.DiaryEntry,
		KGTriples:    triples,
		Verification: v,
	}
}

// resolveOneshotWing resolves the palace wing for the drawer write.
// WriteFinalizeOneshot's sealed signature carries no per-spawn agent
// metadata, so the wing re-resolves through the agents cache (the same
// department+role key the spawn resolved). Every failure mode degrades
// to the empty wing — the ticket-mode behavior for agents without a
// palace_wing.
func resolveOneshotWing(ctx context.Context, deps Deps, row store.SelectScheduledTaskByRunIDRow, logger *slog.Logger) string {
	if deps.AgentsCache == nil {
		return ""
	}
	agent, err := deps.AgentsCache.GetForDepartmentAndRole(ctx, row.DepartmentID, row.RoleSlug)
	if err != nil {
		logger.Warn("oneshot wing resolution failed; drawer lands without a wing",
			"role_slug", row.RoleSlug, "err", err)
		return ""
	}
	if agent.PalaceWing == nil {
		return ""
	}
	return *agent.PalaceWing
}

// serializeOneshotDiary composes the oneshot drawer body — the ticket
// serializer's shape (M2.2.1 FR-263: leading prose so mempalace_search
// lands on the drawer, YAML frontmatter, rationale body) keyed by
// scheduled_task_run_id instead of ticket_id. The leading prose is the
// task's objective template: stable searchable text that exists at
// commit time without re-deriving the rendered brief.
func serializeOneshotDiary(taskName, objective string, runID pgtype.UUID, payload *finalize.FinalizePayload, completedAt time.Time) string {
	var b strings.Builder
	b.WriteString(strings.TrimRight(objective, "\n"))
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "scheduled_task_run_id: %s\n", formatUUID(runID))
	fmt.Fprintf(&b, "scheduled_task: %s\n", escapeYAML(taskName))
	fmt.Fprintf(&b, "outcome: %s\n", escapeYAML(payload.Outcome))
	b.WriteString("artifacts:\n")
	for _, a := range payload.DiaryEntry.Artifacts {
		fmt.Fprintf(&b, diaryListItemFmt, escapeYAML(a))
	}
	b.WriteString("blockers:\n")
	for _, a := range payload.DiaryEntry.Blockers {
		fmt.Fprintf(&b, diaryListItemFmt, escapeYAML(a))
	}
	b.WriteString("discoveries:\n")
	for _, a := range payload.DiaryEntry.Discoveries {
		fmt.Fprintf(&b, diaryListItemFmt, escapeYAML(a))
	}
	fmt.Fprintf(&b, "completed_at: %s\n", completedAt.Format(time.RFC3339))
	b.WriteString("---\n\n")
	b.WriteString(payload.DiaryEntry.Rationale)
	return b.String()
}

// failOneshotWrite emits the failure log, writes the terminal failed
// agent_instances row outside the rolled-back tx (fresh WithoutCancel
// context so supervisor SIGTERM can't strand the row), and returns a
// wrapped error whose string carries the canonical exit_reason — the
// ticket-mode writeFinalizeFailure posture for the oneshot origin.
func failOneshotWrite(parentCtx context.Context, deps Deps, instanceID pgtype.UUID, f oneshotWriteFailure, logger *slog.Logger) error {
	if f.orphan {
		logger.Warn("palace_write_orphaned",
			"failure_class", f.class,
			"err", fmt.Sprintf("%v", f.err),
		)
	}
	level := slog.LevelError
	if f.class == "timeout" {
		level = slog.LevelWarn
	}
	logger.Log(parentCtx, level, "finalize_oneshot_write_failed",
		"exit_reason", f.reason,
		"failure_class", f.class,
		"err", fmt.Sprintf("%v", f.err),
	)

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), TerminalWriteGrace)
	defer cancel()
	reason := f.reason
	if err := deps.Queries.UpdateInstanceTerminalWithCostAndWakeup(termCtx, store.UpdateInstanceTerminalWithCostAndWakeupParams{
		ID:         instanceID,
		Status:     "failed",
		ExitReason: &reason,
	}); err != nil {
		logger.Error("oneshot terminal-failure write failed", "err", err)
	}
	return fmt.Errorf("spawn: finalize_oneshot %s: %w", f.reason, f.err)
}
