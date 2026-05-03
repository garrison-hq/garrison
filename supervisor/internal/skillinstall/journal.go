package skillinstall

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// Step is the strongly-typed name of an install pipeline step.
// Values map 1:1 to the agent_install_journal.step CHECK constraint.
type Step string

const (
	StepDownload        Step = "download"
	StepVerifyDigest    Step = "verify_digest"
	StepExtract         Step = "extract"
	StepMount           Step = "mount"
	StepContainerCreate Step = "container_create"
	StepContainerStart  Step = "container_start"
)

// AllSteps lists every step in canonical order. Resume uses this to
// determine "what's next" given the most-recent journal row.
var AllSteps = []Step{
	StepDownload, StepVerifyDigest, StepExtract,
	StepMount, StepContainerCreate, StepContainerStart,
}

// Outcome is the per-step terminal state.
type Outcome string

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeFailed      Outcome = "failed"
	OutcomeInterrupted Outcome = "interrupted"
)

// Journaler writes per-step audit rows to agent_install_journal and
// reads the most-recent step at restart. Backed by store.Queries in
// production; the journal_test uses a real pgxpool against a
// testcontainer (skillinstall is integration-test scope only since
// every method touches Postgres).
type Journaler struct {
	Queries *store.Queries
}

// RecordStep writes one journal row.
func (j *Journaler) RecordStep(ctx context.Context, proposalID pgtype.UUID, step Step, outcome Outcome, errorKind string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("skillinstall: marshal journal payload: %w", err)
	}
	_, err = j.Queries.InsertInstallStep(ctx, store.InsertInstallStepParams{
		ProposalID:   proposalID,
		Step:         string(step),
		Outcome:      string(outcome),
		ErrorKind:    ptrIfNonEmpty(errorKind),
		PayloadJsonb: body,
	})
	if err != nil {
		return fmt.Errorf("skillinstall: insert install step: %w", err)
	}
	return nil
}

// LatestStep returns the most-recent journal row for proposalID, or
// (nil, nil) if no rows exist (install hasn't started).
func (j *Journaler) LatestStep(ctx context.Context, proposalID pgtype.UUID) (*store.AgentInstallJournal, error) {
	row, err := j.Queries.GetLatestInstallStep(ctx, proposalID)
	if err != nil {
		// pgx.ErrNoRows surfaces as a non-nil err with the standard
		// shape; callers route on (nil, nil) by treating "no row" as
		// "no progress yet" which is the same recovery behaviour.
		return nil, nil
	}
	return &row, nil
}

// SuccessfulStepCount returns how many of the 6 steps for proposalID
// have outcome='success'. Used by Resume to short-circuit when all
// 6 are already done.
func (j *Journaler) SuccessfulStepCount(ctx context.Context, proposalID pgtype.UUID) (int64, error) {
	n, err := j.Queries.CountSuccessfulStepsForProposal(ctx, proposalID)
	if err != nil {
		return 0, fmt.Errorf("skillinstall: count successful steps: %w", err)
	}
	return n, nil
}

func ptrIfNonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
