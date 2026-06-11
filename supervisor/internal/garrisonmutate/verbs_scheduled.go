package garrisonmutate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/schedule"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// scheduledTaskURLPrefix is the dashboard route prefix for scheduled-
// task detail pages (M9's /admin/recurring-jobs/[id] route, T015).
const scheduledTaskURLPrefix = "/admin/recurring-jobs/"

// resourceTypeScheduledTask is the affected_resource_type CHECK enum
// value for the scheduled-task domain. Mirrored from the M9 migration's
// chat_mutation_audit CHECK extension.
const resourceTypeScheduledTask = "scheduled_task"

// verbCreateScheduledTask is the registry name, centralised so the
// handler's audit rows, failure audits, and notify payloads cannot
// drift from the Verbs entry.
const verbCreateScheduledTask = "create_scheduled_task"

// defaultSchedMinInterval mirrors config.SchedMinInterval's default
// (GARRISON_SCHED_MIN_INTERVAL, 15m). The verb runs inside the
// `supervisor mcp garrison-mutate` subprocess, which does not load the
// full supervisor config; it reads the same env var directly and falls
// back to the same default, so both authoring surfaces (chat verb +
// dashboardapi validate endpoint) enforce one bound (FR-404, FR-602).
const defaultSchedMinInterval = 15 * time.Minute

// CreateScheduledTaskArgs is the JSON-encoded input shape for
// create_scheduled_task. Field names match the threat-model amendment's
// verb signature: create_scheduled_task(name, department_slug,
// role_slug, mode, schedule_expr, objective_template,
// acceptance_criteria_template).
type CreateScheduledTaskArgs struct {
	Name                       string `json:"name"`
	DepartmentSlug             string `json:"department_slug"`
	RoleSlug                   string `json:"role_slug"`
	Mode                       string `json:"mode"`
	ScheduleExpr               string `json:"schedule_expr"`
	ObjectiveTemplate          string `json:"objective_template"`
	AcceptanceCriteriaTemplate string `json:"acceptance_criteria_template"`
}

// realCreateScheduledTaskHandler implements
// garrison-mutate.create_scheduled_task (M9 FR-600/FR-602). Tier 3
// reversibility per chat-threat-model.md §5: creates recurring
// cost-incurring state; accrued firings/spend do not reverse. Full args
// land in args_jsonb, anchored on chat_session_id.
//
// Chat-only verb: assertExactlyOneCallerAnchor is deliberately NOT
// applied — AgentInstanceID callers are rejected explicitly with
// validation_failed ("agents cannot schedule work") per the threat-model
// amendment (§3 threat 7). The dispatch layer already hides the verb
// from agent-mode servers (agentVerbNames is create_ticket only); this
// check is defense-in-depth against a wiring regression.
//
// All rejects map to validation_failed + detail (plan decision 13);
// validation is schedule.ValidateTask — the same path the dashboard's
// validate endpoint uses — so chat-created tasks are behaviorally
// indistinguishable from dashboard-created ones (FR-602).
func realCreateScheduledTaskHandler(ctx context.Context, deps Deps, raw json.RawMessage) (Result, error) {
	if deps.AgentInstanceID.Valid {
		return validationFailure("create_scheduled_task: agents cannot schedule work"), nil
	}
	args, vRes := parseCreateScheduledTaskArgs(raw)
	if vRes != nil {
		return *vRes, nil
	}

	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("create_scheduled_task: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := store.New(tx)

	dept, err := q.GetDepartmentBySlug(ctx, args.DepartmentSlug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return validationFailure(fmt.Sprintf("create_scheduled_task: department %q not found", args.DepartmentSlug)),
				writeFailureAudit(ctx, deps, verbCreateScheduledTask, args, ErrValidationFailed, 3, "")
		}
		return Result{}, fmt.Errorf("create_scheduled_task: lookup department: %w", err)
	}

	next, err := schedule.ValidateTask(ctx, q, schedVerbMinInterval(deps), time.Now().UTC(), schedule.ValidationInput{
		Name:               args.Name,
		RoleSlug:           args.RoleSlug,
		ScheduleExpr:       args.ScheduleExpr,
		ObjectiveTemplate:  args.ObjectiveTemplate,
		AcceptanceTemplate: args.AcceptanceCriteriaTemplate,
		DepartmentID:       dept.ID,
		Mode:               args.Mode,
	})
	if err != nil {
		var ve *schedule.ValidationError
		if errors.As(err, &ve) {
			return validationFailure("create_scheduled_task: " + ve.Error()),
				writeFailureAudit(ctx, deps, verbCreateScheduledTask, args, ErrValidationFailed, 3, "")
		}
		return Result{}, fmt.Errorf("create_scheduled_task: validate: %w", err)
	}

	task, err := q.InsertScheduledTask(ctx, store.InsertScheduledTaskParams{
		Name:                       args.Name,
		DepartmentID:               dept.ID,
		RoleSlug:                   args.RoleSlug,
		Mode:                       args.Mode,
		ScheduleExpr:               args.ScheduleExpr,
		NextFireAt:                 pgtype.Timestamptz{Time: next, Valid: true},
		ObjectiveTemplate:          args.ObjectiveTemplate,
		AcceptanceCriteriaTemplate: args.AcceptanceCriteriaTemplate,
	})
	if err != nil {
		return Result{}, fmt.Errorf("create_scheduled_task: insert task: %w", err)
	}

	resourceID := uuidString(task.ID)
	rt := resourceTypeScheduledTask
	if _, err := WriteAudit(ctx, q, AuditWriteParams{
		ChatSessionID:        deps.ChatSessionID,
		ChatMessageID:        deps.ChatMessageID,
		Verb:                 verbCreateScheduledTask,
		Args:                 args,
		Outcome:              "success",
		ReversibilityClass:   3,
		AffectedResourceID:   &resourceID,
		AffectedResourceType: &rt,
	}); err != nil {
		return Result{}, fmt.Errorf("create_scheduled_task: write audit: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("create_scheduled_task: commit: %w", err)
	}

	// Post-commit notify (Rule 6 backstop: IDs only, no chat content).
	emitNotifyBestEffort(deps, "scheduled_task.created", chatNotifyPayload{
		ChatSessionID:        uuidString(deps.ChatSessionID),
		ChatMessageID:        uuidString(deps.ChatMessageID),
		Verb:                 verbCreateScheduledTask,
		AffectedResourceID:   resourceID,
		AffectedResourceType: rt,
	})

	return Result{
		Success:             true,
		AffectedResourceID:  resourceID,
		AffectedResourceURL: scheduledTaskURLPrefix + resourceID,
		Message: fmt.Sprintf(
			"Created scheduled task %q (%s mode, %s) in %s; first firing at %s. The supervisor's tick loop fires it automatically — no further action needed.",
			args.Name, args.Mode, args.ScheduleExpr, args.DepartmentSlug, next.UTC().Format(time.RFC3339),
		),
	}, nil
}

// parseCreateScheduledTaskArgs unmarshals + trims + checks presence of
// the identity fields. Semantic validation (grammar, min-interval,
// future slot, name uniqueness, department/role existence, non-empty
// templates, mode enum) is schedule.ValidateTask's job — duplicating it
// here would let the two authoring surfaces drift.
func parseCreateScheduledTaskArgs(raw json.RawMessage) (CreateScheduledTaskArgs, *Result) {
	var args CreateScheduledTaskArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		r := validationFailure("create_scheduled_task: parse args: " + err.Error())
		return CreateScheduledTaskArgs{}, &r
	}
	args.Name = strings.TrimSpace(args.Name)
	args.DepartmentSlug = strings.TrimSpace(args.DepartmentSlug)
	args.RoleSlug = strings.TrimSpace(args.RoleSlug)
	args.Mode = strings.TrimSpace(args.Mode)
	args.ScheduleExpr = strings.TrimSpace(args.ScheduleExpr)
	required := []struct{ field, value string }{
		{"name", args.Name},
		{"department_slug", args.DepartmentSlug},
		{"role_slug", args.RoleSlug},
		{"schedule_expr", args.ScheduleExpr},
	}
	for _, f := range required {
		if f.value == "" {
			r := validationFailure("create_scheduled_task: " + f.field + " is required")
			return CreateScheduledTaskArgs{}, &r
		}
	}
	return args, nil
}

// schedVerbMinInterval resolves the minimum firing interval bound
// (FR-404) inside the garrison-mutate subprocess: the
// GARRISON_SCHED_MIN_INTERVAL env var when parseable and positive,
// config's default otherwise. Malformed values degrade to the default
// with a warning rather than failing the verb — the supervisor's own
// config.Load rejects them at boot, so a bad value here means the
// subprocess env diverged from the supervisor's.
func schedVerbMinInterval(deps Deps) time.Duration {
	v := os.Getenv("GARRISON_SCHED_MIN_INTERVAL")
	if v == "" {
		return defaultSchedMinInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		if deps.Logger != nil {
			deps.Logger.Warn("create_scheduled_task: invalid GARRISON_SCHED_MIN_INTERVAL; using default",
				"value", v, "default", defaultSchedMinInterval.String())
		}
		return defaultSchedMinInterval
	}
	return d
}

// init wires the real handler into the registry, replacing the
// stubHandler placeholder verbs.go declares at package init.
func init() {
	handleCreateScheduledTask = realCreateScheduledTaskHandler
}
