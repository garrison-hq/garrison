package hygiene

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/pgdb"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Deps bundles the hygiene subsystem's runtime collaborators. Constructed
// by cmd/supervisor/main.go (T015) and handed to both RunListener and
// RunSweep verbatim.
type Deps struct {
	// DSN is the garrison_agent_mempalace DSN (SELECT-only). Used by both
	// RunListener (to open a dedicated LISTEN conn) and RunSweep (to open
	// its own short-lived conn).
	DSN string

	// Dialer opens pgx connections. Production: pgdb.NewRealDialer(); tests
	// substitute a fake.
	Dialer pgdb.Dialer

	// Queries is a pool-bound *store.Queries. The hygiene writer path
	// (UpdateTicketTransitionHygiene) uses it directly; at-most-once-to-
	// terminal is enforced inside the query's WHERE clause per FR-215.
	Queries *store.Queries

	// Palace is the palace-query client (see palace.go).
	Palace *Client

	// Logger — optional; defaults to slog.Default().
	Logger *slog.Logger

	// Delay is the wait between notification arrival and evaluation
	// (FR-212; default 5s). Tunable via GARRISON_HYGIENE_DELAY; populated
	// from config.Config in main.go.
	Delay time.Duration

	// SweepInterval is the cadence of the periodic sweep (FR-216; default
	// 60s). Tunable via GARRISON_HYGIENE_SWEEP_INTERVAL.
	SweepInterval time.Duration

	// TerminalWriteGrace bounds the in-flight UPDATE's context after root
	// ctx cancellation (FR-217). Inherits spawn.TerminalWriteGrace.
	TerminalWriteGrace time.Duration

	// Channels lists the LISTEN channels to subscribe to. M2.2 registers
	// exactly ["work.ticket.transitioned.engineering.in_dev.qa_review",
	// "work.ticket.transitioned.engineering.qa_review.done"] but the slice
	// shape lets future milestones add more without touching this package.
	Channels []string
}

// transitionNotifyPayload mirrors the JSON the emit_ticket_transitioned
// trigger pushes through pg_notify per the M2.2 migration. Field names
// match the jsonb_build_object invocation there.
type transitionNotifyPayload struct {
	TransitionID    pgtype.UUID `json:"transition_id"`
	TicketID        pgtype.UUID `json:"ticket_id"`
	AgentInstanceID pgtype.UUID `json:"agent_instance_id"`
	FromColumn      string      `json:"from_column"`
	ToColumn        string      `json:"to_column"`
}

// RunListener opens a dedicated *pgx.Conn for LISTEN, subscribes to all
// channels in Deps.Channels, and dispatches each notification to the
// evaluation pipeline. Reconnects on transient pgx errors with exp
// backoff (100ms → 30s cap), identical to the M1 events listener pattern.
// Returns only on root-ctx cancellation.
func RunListener(ctx context.Context, deps Deps) error {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	backoff := 100 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		conn, err := deps.Dialer.DialConn(ctx, deps.DSN)
		if err != nil {
			deps.Logger.Warn("hygiene listener dial failed; retrying",
				"err", err, "backoff", backoff)
			if sleepCtx(ctx, backoff) != nil {
				return ctx.Err()
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// Successful dial resets backoff.
		backoff = 100 * time.Millisecond

		err = listenLoop(ctx, conn, deps)
		_ = conn.Close(context.WithoutCancel(ctx))
		if ctx.Err() != nil {
			return ctx.Err()
		}
		deps.Logger.Warn("hygiene listener lost connection; reconnecting",
			"err", err, "backoff", backoff)
		if sleepCtx(ctx, backoff) != nil {
			return ctx.Err()
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// listenLoop issues LISTEN for every configured channel and then loops on
// WaitForNotification, dispatching each payload through evaluateAndWrite.
// Returns on the first conn error so the caller can reconnect; ctx
// cancellation is propagated via conn.WaitForNotification returning.
func listenLoop(ctx context.Context, conn *pgx.Conn, deps Deps) error {
	for _, ch := range deps.Channels {
		stmt := fmt.Sprintf(`LISTEN "%s"`, ch)
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("hygiene LISTEN %q: %w", ch, err)
		}
		deps.Logger.Info("hygiene LISTEN started", "channel", ch)
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("hygiene WaitForNotification: %w", err)
		}
		// pg_notify payload from the trigger is the event_outbox event_id
		// (plan §"emit_ticket_transitioned"). To get the full
		// transitionNotifyPayload, we'd need to SELECT from event_outbox
		// using the event_id. BUT the trigger also inserts the transition
		// row itself — so we go through the transition_id directly. See
		// handleTransition for the lookup discipline.
		go func(payload, channel string) {
			if err := handleTransition(ctx, deps, payload, channel); err != nil {
				deps.Logger.Warn("hygiene handleTransition failed; sweep will retry",
					"channel", channel,
					"err", err,
				)
			}
		}(n.Payload, n.Channel)
	}
}

// handleTransition is the per-notification pipeline. Payload is the
// jsonb_build_object('event_id', ...)::text string the trigger emits.
// We look up the event_outbox row to get the real payload, then run
// evaluateAndWrite after Deps.Delay.
//
// On any palace-side failure, evaluateAndWrite writes StatusPending and
// the sweep will retry. Connection/query failures bubble up so the
// caller can log.
func handleTransition(ctx context.Context, deps Deps, payload, channel string) error {
	// Per M1/M2.1 pg_notify convention, the payload carries {event_id: "<uuid>"}.
	var p struct {
		EventID pgtype.UUID `json:"event_id"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return fmt.Errorf("decode notify payload: %w", err)
	}
	if !p.EventID.Valid {
		return errors.New("notify payload has no event_id")
	}

	// The trigger wrote the full transition payload into event_outbox;
	// fetch it. This is a SELECT, permitted by the SELECT-only grant.
	evt, err := deps.Queries.LockEventForProcessing(ctx, p.EventID)
	if err != nil {
		return fmt.Errorf("fetch event_outbox: %w", err)
	}
	var tp transitionNotifyPayload
	if err := json.Unmarshal(evt.Payload, &tp); err != nil {
		return fmt.Errorf("decode transition payload: %w", err)
	}

	// Delay to let the agent's post-transition palace writes land.
	if sleepCtx(ctx, deps.Delay) != nil {
		return ctx.Err()
	}
	return evaluateAndWrite(ctx, deps, tp.TransitionID, tp.TicketID, tp.AgentInstanceID)
}

// evaluateAndWrite is the shared pipeline for both the LISTEN path and
// the sweep path. Resolves the run window, queries the palace, evaluates,
// and writes the hygiene_status via the at-most-once-to-terminal UPDATE.
//
// Transition rows whose triggered_by_agent_instance_id is NULL (test
// fixtures; manual inserts) are skipped per the Edge Cases list — we
// can't resolve the run window without an agent_instance_id.
func evaluateAndWrite(ctx context.Context, deps Deps, transitionID, ticketID, agentInstanceID pgtype.UUID) error {
	if !agentInstanceID.Valid {
		deps.Logger.Info("hygiene skipped: transition has no agent_instance_id",
			"ticket_transition_id", uuidText(transitionID),
			"ticket_id", uuidText(ticketID),
		)
		return nil
	}

	// M2.2.1 T008: route finalize-shaped rows to the pure-Go
	// EvaluateFinalizeOutcome; legacy M2.2 rows continue through the
	// palace-query-based Evaluate. The routing key is the agent_instances
	// exit_reason: IsFinalizeExitReason distinguishes the two paths.
	// SelectAgentInstanceFinalizedState returns (status, exit_reason,
	// has_transition) in one round-trip.
	finalized, err := deps.Queries.SelectAgentInstanceFinalizedState(ctx, agentInstanceID)
	if err != nil {
		return fmt.Errorf("SelectAgentInstanceFinalizedState: %w", err)
	}
	exitReason := ""
	if finalized.ExitReason != nil {
		exitReason = *finalized.ExitReason
	}

	// M2.2.1 finalize-shaped rows: pure-Go evaluation, no palace query.
	if IsFinalizeExitReason(exitReason) {
		status := EvaluateFinalizeOutcome(AgentInstanceFinalizeSignal{
			ExitReason:    exitReason,
			HasTransition: finalized.HasTransition,
		})
		writeCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			deps.TerminalWriteGrace,
		)
		defer cancel()
		if err := deps.Queries.UpdateTicketTransitionHygiene(writeCtx, store.UpdateTicketTransitionHygieneParams{
			ID:            transitionID,
			HygieneStatus: ptr(string(status)),
		}); err != nil {
			deps.Logger.Warn("hygiene UPDATE failed",
				"ticket_transition_id", uuidText(transitionID),
				"ticket_id", uuidText(ticketID),
				"agent_instance_id", uuidText(agentInstanceID),
				"intended_status", string(status),
				"err", err,
			)
			return err
		}
		deps.Logger.Info("hygiene evaluated (finalize path)",
			"ticket_transition_id", uuidText(transitionID),
			"ticket_id", uuidText(ticketID),
			"agent_instance_id", uuidText(agentInstanceID),
			"exit_reason", exitReason,
			"hygiene_status", string(status),
		)
		return nil
	}

	// Legacy M2.2 path below: palace query + Evaluate.
	win, err := deps.Queries.GetAgentInstanceRunWindow(ctx, agentInstanceID)
	if err != nil {
		return fmt.Errorf("GetAgentInstanceRunWindow: %w", err)
	}
	palaceWing := ""
	if win.PalaceWing != nil {
		palaceWing = *win.PalaceWing
	}

	ticketIDText := "ticket_" + uuidText(ticketID)

	var windowEnd time.Time
	if win.FinishedAt.Valid {
		windowEnd = win.FinishedAt.Time
	}
	if !win.StartedAt.Valid {
		return errors.New("agent_instance started_at is null")
	}
	queryWin := TimeWindow{Start: win.StartedAt.Time, End: windowEnd}

	drawers, triples, palaceErr := deps.Palace.Query(ctx, ticketIDText, palaceWing, queryWin)

	status := Evaluate(EvaluationInput{
		TicketIDText:   ticketIDText,
		RunWindowStart: win.StartedAt.Time,
		RunWindowEnd:   windowEnd,
		PalaceWing:     palaceWing,
		Drawers:        drawers,
		KGTriples:      triples,
		PalaceErr:      palaceErr,
	})

	// Terminal-write discipline per FR-217: use a detached ctx plus the
	// TerminalWriteGrace cap so a shutdown mid-evaluation doesn't orphan
	// a row. When ctx is live, WithoutCancel is a no-op except for the
	// timeout overlay.
	writeCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		deps.TerminalWriteGrace,
	)
	defer cancel()

	if err := deps.Queries.UpdateTicketTransitionHygiene(writeCtx, store.UpdateTicketTransitionHygieneParams{
		ID:            transitionID,
		HygieneStatus: ptr(string(status)),
	}); err != nil {
		deps.Logger.Warn("hygiene UPDATE failed",
			"ticket_transition_id", uuidText(transitionID),
			"ticket_id", uuidText(ticketID),
			"agent_instance_id", uuidText(agentInstanceID),
			"intended_status", string(status),
			"err", err,
		)
		return err
	}
	deps.Logger.Info("hygiene evaluated",
		"ticket_transition_id", uuidText(transitionID),
		"ticket_id", uuidText(ticketID),
		"agent_instance_id", uuidText(agentInstanceID),
		"hygiene_status", string(status),
	)
	return nil
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns ctx.Err() on
// cancellation, nil otherwise.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// uuidText stringifies a pgtype.UUID in 8-4-4-4-12 hex form.
func uuidText(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	out := make([]byte, 36)
	const hex = "0123456789abcdef"
	j := 0
	for i, x := range b {
		out[j] = hex[x>>4]
		j++
		out[j] = hex[x&0x0f]
		j++
		if i == 3 || i == 5 || i == 7 || i == 9 {
			out[j] = '-'
			j++
		}
	}
	return strings.ToLower(string(out))
}

func ptr[T any](v T) *T { return &v }
