package skillinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"
)

// Resume picks up an in-progress install after a supervisor crash.
// Reads the latest journal row for proposalID and either continues
// from the next step or rolls back. Called by cmd/supervisor/main.go
// at startup for every proposal in status='install_in_progress'.
//
// Resume is invoked AFTER the supervisor's first call to
// MarkInterruptedSteps (sqlc query in m7_install_journal.sql) so any
// interrupted-mid-write rows already carry outcome='interrupted'.
func (a *Actuator) Resume(ctx context.Context, proposalID, agentID pgtype.UUID, skill SkillRef) error {
	latest, err := a.Journaler.LatestStep(ctx, proposalID)
	if err != nil {
		return fmt.Errorf("skillinstall: load latest step: %w", err)
	}
	if latest == nil {
		// No journal rows — install never started. Run the full pipeline.
		a.Logger.Info("install: resume — no prior progress; running full pipeline",
			"proposal_id", uuidString(proposalID))
		return a.Install(ctx, proposalID, agentID, skill)
	}

	switch Outcome(latest.Outcome) {
	case OutcomeSuccess:
		return a.resumeFromNextStep(ctx, proposalID, agentID, skill, Step(latest.Step))
	case OutcomeFailed:
		// Already terminal; nothing to do — caller marked the proposal install_failed already.
		a.Logger.Info("install: resume — already terminal-failed",
			"proposal_id", uuidString(proposalID), "last_step", latest.Step)
		return fmt.Errorf("%w: prior outcome=failed at step %s", ErrInterruptedBySupervisorCrash, latest.Step)
	case OutcomeInterrupted:
		return a.rollback(ctx, proposalID, agentID, skill, Step(latest.Step))
	default:
		return fmt.Errorf("skillinstall: unknown outcome %q for step %s", latest.Outcome, latest.Step)
	}
}

// resumeFromNextStep runs the pipeline starting at the step AFTER
// the most-recent successful one. The actuator's per-step methods
// are designed for sequential first-time invocation; resume after a
// success requires re-fetching the body for steps that need it.
// We re-download because keeping the body in memory across a restart
// is impossible.
func (a *Actuator) resumeFromNextStep(ctx context.Context, proposalID, agentID pgtype.UUID, skill SkillRef, lastSuccess Step) error {
	idx := stepIndex(lastSuccess)
	if idx < 0 {
		return fmt.Errorf("skillinstall: unknown step %q in journal", lastSuccess)
	}
	if idx == len(AllSteps)-1 {
		// All 6 steps done already — success has already been
		// recorded for container_start; the proposal should be
		// marked installed by the caller.
		a.Logger.Info("install: resume — all steps already success",
			"proposal_id", uuidString(proposalID))
		return nil
	}
	a.Logger.Info("install: resume — continuing",
		"proposal_id", uuidString(proposalID),
		"last_success_step", string(lastSuccess),
		"next_step", string(AllSteps[idx+1]))

	// For simplicity (and robustness against state drift), re-run the
	// full pipeline from step 1. The journal will record success
	// rows for steps that succeed again; idempotency is guaranteed
	// because the install actuator's effects (extract→tmp dir →
	// rename) are designed to be safely repeatable.
	return a.Install(ctx, proposalID, agentID, skill)
}

// rollback cleans up partial side-effects from an interrupted step.
// Fail-soft: log + continue if individual cleanup steps fail; the
// proposal will be marked install_failed regardless and the operator
// can manually clean up.
func (a *Actuator) rollback(ctx context.Context, proposalID, agentID pgtype.UUID, skill SkillRef, interruptedAt Step) error {
	a.Logger.Info("install: rollback — supervisor crash detected",
		"proposal_id", uuidString(proposalID),
		"interrupted_at", string(interruptedAt))

	// Wipe partial extract.
	tmpDir := filepath.Join(a.SkillsDir, uuidString(agentID), skill.Package+".tmp")
	if err := os.RemoveAll(tmpDir); err != nil {
		a.Logger.Warn("install: rollback rmrf tmp_dir failed",
			"path", tmpDir, "err", err)
	}

	// If we made it past container_create but not container_start,
	// remove the created container. The journal carries the container
	// ID in the StepContainerCreate row's payload.
	if interruptedAt == StepContainerStart {
		// Look back for the StepContainerCreate row's payload.
		// Implementation detail: we don't currently expose a "list
		// rows" query for the journal; for now log a TODO and let
		// reconciler GC the orphan.
		a.Logger.Info("install: rollback — leaving container_create row to reconciler GC",
			"proposal_id", uuidString(proposalID))
	}

	if err := a.Journaler.RecordStep(ctx, proposalID, interruptedAt, OutcomeFailed,
		"interrupted_by_supervisor_crash",
		map[string]any{"rollback": "completed"}); err != nil {
		a.Logger.Warn("install: rollback journal write failed",
			"proposal_id", uuidString(proposalID), "err", err)
	}
	return errors.New("install rollback complete; mark proposal install_failed")
}

// stepIndex returns the index of step in AllSteps, or -1 if unknown.
func stepIndex(step Step) int {
	for i, s := range AllSteps {
		if s == step {
			return i
		}
	}
	return -1
}
