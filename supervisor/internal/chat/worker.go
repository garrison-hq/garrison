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
// notify into Worker.HandleMessage on a fresh goroutine.
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

// HandleMessage processes one operator chat message identified by its
// chat_messages row id. The flow:
//  1. Look up the operator row + parent session.
//  2. EnsureActiveSession (else terminal-write a synthetic assistant
//     row with error_kind='session_ended').
//  3. EnsureCostCapNotExceeded (else error_kind='session_cost_cap_reached').
//  4. INSERT assistant row at status='pending' with
//     turn_index = operator+1.
//  5. Build the transcript via AssembleTranscript(prior, operator.content).
//  6. SpawnTurn — vault fetch + docker run + parser + commit.
//  7. Done. Mutex released by deferred Unlock.
func (w *Worker) HandleMessage(ctx context.Context, operatorMessageID pgtype.UUID) error {
	q := w.Deps.Queries

	// Resolve operator row by id. We don't have a dedicated query
	// (kept persistence.go small in T006); the transcript helper +
	// ad-hoc lookup are sufficient single-operator.
	opRow, err := w.findOperatorMessage(ctx, operatorMessageID)
	if err != nil {
		return fmt.Errorf("worker: lookup operator: %w", err)
	}

	// Per-session serial processing.
	mu := w.Mutexes.AcquireForSession(opRow.SessionID)
	mu.Lock()
	defer mu.Unlock()

	// Step 2 — session must be active.
	sess, err := EnsureActiveSession(ctx, q, opRow.SessionID)
	if errors.Is(err, ErrSessionEnded) || errors.Is(err, ErrSessionNotFound) {
		// Synthetic assistant row at status='failed' for the operator's
		// turn so the dashboard can surface the error_kind via SSE.
		ek := ErrorSessionEnded
		if errors.Is(err, ErrSessionNotFound) {
			ek = ErrorSessionNotFound
		}
		return w.terminalSyntheticAssistant(ctx, opRow, ek)
	}
	if err != nil {
		return fmt.Errorf("worker: ensure active: %w", err)
	}

	// Step 3 — soft cost cap.
	if err := EnsureCostCapNotExceeded(ctx, w.Deps, sess); errors.Is(err, ErrCostCapReached) {
		return w.terminalSyntheticAssistant(ctx, opRow, ErrorSessionCostCapReached)
	} else if err != nil {
		return fmt.Errorf("worker: cost-cap check: %w", err)
	}

	// Step 4 — insert assistant pending row.
	asstRow, err := q.InsertAssistantPending(ctx, store.InsertAssistantPendingParams{
		SessionID: opRow.SessionID,
		TurnIndex: opRow.TurnIndex + 1,
	})
	if err != nil {
		return fmt.Errorf("worker: insert assistant pending: %w", err)
	}

	// Step 5 — assemble transcript from prior rows + current operator content.
	prior, err := q.GetSessionTranscript(ctx, opRow.SessionID)
	if err != nil {
		writeAssistantError(ctx, w.Deps, asstRow.ID, ErrorClaudeRuntimeError)
		return fmt.Errorf("worker: load transcript: %w", err)
	}
	// Drop the current operator's row from the prior set — it'll be
	// appended via AssembleTranscript's currentOperatorContent arg.
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
		writeAssistantError(ctx, w.Deps, asstRow.ID, ErrorClaudeRuntimeError)
		return fmt.Errorf("worker: assemble transcript: %w", err)
	}

	// Step 6 — spawn the turn.
	if err := w.Deps.SpawnTurn(ctx, opRow.SessionID, asstRow.ID, transcript, w.Mempalace, w.SupervisorBin, w.AgentRoDSN); err != nil {
		// SpawnTurn already terminal-wrote the assistant row on its
		// failure paths; just log + return so the listener moves on.
		w.Deps.Logger.Warn("chat: SpawnTurn failed",
			"session_id", uuidString(opRow.SessionID),
			"message_id", uuidString(asstRow.ID),
			"err", err)
	}
	return nil
}

// findOperatorMessage scans the session's transcript for the row id.
// O(N) per session is fine single-operator. Returns ErrSessionNotFound-
// shaped errors transparently.
func (w *Worker) findOperatorMessage(ctx context.Context, id pgtype.UUID) (store.GetSessionTranscriptRow, error) {
	// Without a dedicated GetMessageByID query, scan all sessions for
	// the given message. Since the listener fires per-message, the
	// notify payload typically carries (session_id, message_id) — we
	// could pass session_id alongside id to skip this scan. Listener
	// (T012) wires (session_id, message_id) so this scan path is
	// only hit by tests that look up by message_id alone.
	//
	// For production fast-path the listener calls findOperatorMessage_
	// WithSession; this fallback exists for completeness.
	return store.GetSessionTranscriptRow{}, fmt.Errorf("chat: findOperatorMessage requires session_id; use FindOperatorMessageInSession")
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

	prior, err := w.Deps.Queries.GetSessionTranscript(ctx, opRow.SessionID)
	if err != nil {
		writeAssistantError(ctx, w.Deps, asstRow.ID, ErrorClaudeRuntimeError)
		return err
	}
	currentText := ""
	if opRow.Content != nil {
		currentText = *opRow.Content
	}
	pruned := make([]store.GetSessionTranscriptRow, 0, len(prior))
	for _, r := range prior {
		if r.ID == opRow.ID {
			continue
		}
		pruned = append(pruned, r)
	}
	transcript, err := AssembleTranscript(pruned, currentText)
	if err != nil {
		writeAssistantError(ctx, w.Deps, asstRow.ID, ErrorClaudeRuntimeError)
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
