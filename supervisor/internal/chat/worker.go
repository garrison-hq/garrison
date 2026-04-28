package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Worker is the per-message orchestration handle. The chat.Listener
// constructs one Worker at boot and dispatches each chat.message.sent
// notify into Worker.HandleMessageInSession on a fresh goroutine.
type Worker struct {
	Deps          Deps
	Mutexes       *sessionMutexRegistry
	SupervisorBin string
	AgentRoDSN    string
	Mempalace     MempalaceWiring
}

// NewWorker constructs the worker. Mutexes can be nil; a fresh
// registry is created in that case.
func NewWorker(deps Deps, supervisorBin, agentRoDSN string, mempalace MempalaceWiring) *Worker {
	return &Worker{
		Deps:          deps,
		Mutexes:       newSessionMutexRegistry(),
		SupervisorBin: supervisorBin,
		AgentRoDSN:    agentRoDSN,
		Mempalace:     mempalace,
	}
}

// buildTranscript loads the prior transcript for the operator's session,
// removes the operator row itself (it's appended via the
// AssembleTranscript currentOperatorContent arg), and returns the
// assembled transcript. On any failure it terminal-writes the assistant
// row with ErrorClaudeRuntimeError so the dashboard surfaces a kind.
func (w *Worker) buildTranscript(
	ctx context.Context,
	opRow store.GetSessionTranscriptRow,
	assistantRowID pgtype.UUID,
) ([]byte, error) {
	prior, err := w.Deps.Queries.GetSessionTranscript(ctx, opRow.SessionID)
	if err != nil {
		writeAssistantError(ctx, w.Deps, assistantRowID, ErrorClaudeRuntimeError)
		return nil, fmt.Errorf("worker: load transcript: %w", err)
	}
	currentText := ""
	if opRow.Content != nil {
		currentText = *opRow.Content
	}
	priorWithoutCurrent := make([]store.GetSessionTranscriptRow, 0, len(prior))
	for _, r := range prior {
		if r.ID == opRow.ID {
			continue
		}
		priorWithoutCurrent = append(priorWithoutCurrent, r)
	}
	transcript, err := AssembleTranscript(priorWithoutCurrent, currentText)
	if err != nil {
		writeAssistantError(ctx, w.Deps, assistantRowID, ErrorClaudeRuntimeError)
		return nil, fmt.Errorf("worker: assemble transcript: %w", err)
	}
	return transcript, nil
}

// FindOperatorMessageInSession is the listener's fast-path lookup:
// it requires the parent session_id (carried in the notify payload)
// so we can scope the transcript scan to one session.
func (w *Worker) FindOperatorMessageInSession(ctx context.Context, sessionID, messageID pgtype.UUID) (store.GetSessionTranscriptRow, error) {
	rows, err := w.Deps.Queries.GetSessionTranscript(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.GetSessionTranscriptRow{}, ErrSessionNotFound
		}
		return store.GetSessionTranscriptRow{}, err
	}
	for _, r := range rows {
		if r.ID == messageID {
			return r, nil
		}
	}
	return store.GetSessionTranscriptRow{}, ErrSessionNotFound
}

// HandleMessageInSession is the listener-friendly entrypoint that
// already knows the session_id; uses FindOperatorMessageInSession.
func (w *Worker) HandleMessageInSession(ctx context.Context, sessionID, operatorMessageID pgtype.UUID) error {
	opRow, err := w.FindOperatorMessageInSession(ctx, sessionID, operatorMessageID)
	if err != nil {
		w.Deps.Logger.Error("chat: operator message not found",
			"session_id", uuidString(sessionID),
			"message_id", uuidString(operatorMessageID),
			"err", err)
		return err
	}

	mu := w.Mutexes.AcquireForSession(opRow.SessionID)
	mu.Lock()
	defer mu.Unlock()

	sess, err := EnsureActiveSession(ctx, w.Deps.Queries, opRow.SessionID)
	if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrSessionNotFound) {
		ek := ErrorSessionEnded
		if errors.Is(err, ErrSessionNotFound) {
			ek = ErrorSessionNotFound
		}
		return w.terminalSyntheticAssistant(ctx, opRow, ek)
	}
	if err != nil {
		return err
	}
	if err := EnsureCostCapNotExceeded(ctx, w.Deps, sess); errors.Is(err, ErrCostCapReached) {
		return w.terminalSyntheticAssistant(ctx, opRow, ErrorSessionCostCapReached)
	}

	asstRow, err := w.Deps.Queries.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: opRow.SessionID,
		TurnIndex: opRow.TurnIndex + 1,
	})
	if err != nil {
		return err
	}

	transcript, err := w.buildTranscript(ctx, opRow, asstRow.ID)
	if err != nil {
		return err
	}

	return w.Deps.SpawnTurn(ctx, opRow.SessionID, asstRow.ID, transcript,
		w.Mempalace, w.SupervisorBin, w.AgentRoDSN)
}

// terminalSyntheticAssistant inserts an assistant row at turn_index +1
// and immediately marks it failed with the supplied error_kind. Used
// when the spawn never gets to start (session ended / cost cap).
func (w *Worker) terminalSyntheticAssistant(ctx context.Context, opRow store.GetSessionTranscriptRow, ek ErrorKind) error {
	asst, err := w.Deps.Queries.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: opRow.SessionID,
		TurnIndex: opRow.TurnIndex + 1,
	})
	if err != nil {
		return err
	}
	writeAssistantError(ctx, w.Deps, asst.ID, ek)
	return nil
}
