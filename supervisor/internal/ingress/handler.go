package ingress

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/throttle"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxBodyBytes is the LimitReader cap applied to every incoming webhook
// body (GitHub's hard cap is 25 MB; we add 1 MB of slack). A body over
// this limit is truncated before HMAC verification, causing a 401 —
// the safe fail-closed behaviour for an oversized payload (FR-800,
// threat-model DoS line, plan §maxBodyBytes).
const maxBodyBytes = 26_214_400 // 26 MB

// HandlerDeps are the per-request dependencies wired by ingress.Server
// (T008). They are defined here so handler.go is self-contained and
// compiles independently of server.go / config.go (T008).
type HandlerDeps struct {
	// Pool is the pgxpool used to Begin each webhook transaction.
	Pool *pgxpool.Pool
	// Queries is the supervisor sqlc query set, used with Queries.WithTx
	// inside the per-request transaction and directly for throttle writes
	// (throttle events are NOT inside the delivery tx — the cap fires before
	// any delivery row is created, FR-602).
	Queries *store.Queries
	// Connector is the validated, wired connector for this handler
	// (e.g. *GitHubConnector). The handler owns the framework spine;
	// the connector owns the provider-specific decisions.
	Connector Connector
	// Secret is the raw HMAC key fetched from vault at boot (FR-302,
	// plan decision 12). Passed to Connector.VerifySignature.
	Secret []byte
	// RejectionCounter is incremented on every 401 (bad/missing
	// signature) without writing a DB row (FR-301). Exposed via
	// GET /ingress/status on the dashboard-api port (plan R3).
	RejectionCounter *atomic.Int64
	// Logger is the structured logger; defaults to slog.Default() when nil.
	Logger *slog.Logger

	// RateCap is the per-connector token bucket (T007). nil disables rate
	// capping (e.g. in unit tests that do not exercise step 7). When non-nil,
	// the connector ID must have been registered via RateCap.AddConnector
	// before the handler is invoked.
	RateCap *RateCap
	// CompanyID is the company UUID resolved once at supervisor boot via
	// SELECT id FROM companies LIMIT 1 (the M6 pattern). Required when
	// RateCap is non-nil — passed to throttle.FireIngressRateCap on a cap
	// breach so no per-request company query is needed (plan §rate cap, R3).
	CompanyID pgtype.UUID
	// RatePerMin and Burst are the connector's configured rate parameters,
	// forwarded to throttle.FireIngressRateCap for forensic payload clarity.
	RatePerMin int
	Burst      int
}

// newWebhookHandler returns an http.HandlerFunc that implements the SR6
// pipeline for one connector (plan §"Handler pipeline — the SR6 order").
//
// Steps implemented in T006 (steps 1–6 and 8–9 of the nine-step pipeline;
// step 7 rate-cap is a no-op pass-through that T007 will fill in):
//
//  1. Read raw body via LimitReader (maxBodyBytes). Raw body FIRST,
//     before any JSON parse (spike F1.4, FR-300 edge case).
//  2. EventType: absent/malformed header → 200 discard.
//  3. Subscribed: unsubscribed event type → 200 discard (SR6 step 1).
//  4. VerifySignature: bad/missing → 401, increment RejectionCounter (FR-300).
//  5. Filter: FilterDiscard → 200 discard (SR6 steps 3–4).
//  6. DeliveryID: absent → 400 (ErrMalformedDelivery).
//  7. (rate cap — no-op in T006; wired in T007)
//  8. Map: ErrNoMapping → 200 discard; other error → 500.
//  9. BEGIN tx: InsertIngressDelivery → 23505 → ROLLBACK, 200;
//     SelectDepartmentIDBySlug; InsertIngressTicket;
//     BackfillIngressDeliveryTicket; COMMIT → 202.
func newWebhookHandler(deps HandlerDeps) http.HandlerFunc {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	conn := deps.Connector

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Step 1: capture raw body FIRST, before any parse.
		// LimitReader guards against oversized payloads (FR-800).
		rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
		if err != nil {
			logger.Error("ingress: read body failed", "error", err)
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}

		// Step 2: extract event type. ok=false → discard 200 (SR6 step 2 prose).
		eventType, ok := conn.EventType(r)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Step 3: check subscription BEFORE signature verification (SR6 step 1).
		// Unsubscribed event types are discarded 200 without spending time on HMAC.
		if !conn.Subscribed(eventType) {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Step 4: signature verification — fail-closed on bad/missing header
		// (FR-300, SR1). Increments RejectionCounter without writing any row
		// (FR-301). Uses the raw body captured in step 1 (spike F1.4).
		if err := conn.VerifySignature(rawBody, r, deps.Secret); err != nil {
			if deps.RejectionCounter != nil {
				deps.RejectionCounter.Add(1)
			}
			logger.Warn("ingress: signature rejected", "connector", conn.ID(), "event_type", eventType)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Step 5: noise filter (SR6 steps 3–4: bot sender, action subtype, ping).
		// FilterDiscard → 200, no ticket, no idempotency row.
		decision, filterErr := conn.Filter(eventType, rawBody)
		if filterErr != nil {
			logger.Warn("ingress: filter parse error; discarding", "connector", conn.ID(), "event_type", eventType, "error", filterErr)
		}
		if decision == FilterDiscard {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Step 6: extract idempotency key. Absent → 400 (ErrMalformedDelivery).
		deliveryID := conn.DeliveryID(r)
		if deliveryID == "" {
			http.Error(w, "missing X-GitHub-Delivery", http.StatusBadRequest)
			return
		}

		// Step 7: per-connector rate cap (FR-600, FR-601, FR-602).
		// The cap fires BEFORE the delivery-row insert so an over-cap delivery
		// writes no ingress_deliveries row; a later legitimate redelivery of the
		// same GUID is therefore treated as fresh and dedups correctly (plan R1).
		if deps.RateCap != nil && !deps.RateCap.Allow(conn.ID()) {
			// Write M6 evidence (throttle_events row + work.throttle.event notify)
			// using the pool-level Queries — not inside the (not-yet-opened) tx.
			if err := throttle.FireIngressRateCap(ctx, deps.Queries, deps.CompanyID, conn.ID(), deps.RatePerMin, deps.Burst); err != nil {
				logger.Error("ingress: FireIngressRateCap failed", "connector", conn.ID(), "error", err)
				// Still return 429 — the cap is enforced regardless of evidence-write outcome.
			}
			http.Error(w, "rate cap exceeded", http.StatusTooManyRequests)
			return
		}

		// Step 8: map event to TicketDraft.
		draft, mapErr := conn.Map(eventType, rawBody)
		if mapErr != nil {
			if errors.Is(mapErr, ErrNoMapping) {
				// No route configured for this event type → 200 discard.
				w.WriteHeader(http.StatusOK)
				return
			}
			logger.Error("ingress: map failed", "connector", conn.ID(), "event_type", eventType, "error", mapErr)
			http.Error(w, "mapping error", http.StatusInternalServerError)
			return
		}

		// Step 9: the idempotency-anchored ticket transaction (plan decision 4).
		//
		// Transaction order (binding per plan §"Idempotency-vs-ticket transaction order"):
		//   a. InsertIngressDelivery → 23505 → ROLLBACK, 200 (ErrDuplicateDelivery).
		//   b. SelectDepartmentIDBySlug → resolve dept UUID.
		//   c. InsertIngressTicket → emit_ticket_created trigger fires (FR-101).
		//   d. BackfillIngressDeliveryTicket → link delivery → ticket.
		//   COMMIT → 202.
		if err := runIngressTx(ctx, deps.Pool, deps.Queries, conn.ID(), deliveryID, draft, logger); err != nil {
			if errors.Is(err, ErrDuplicateDelivery) {
				// Duplicate delivery — already processed; return 200 with no side effects (FR-202).
				w.WriteHeader(http.StatusOK)
				return
			}
			logger.Error("ingress: transaction failed", "connector", conn.ID(), "delivery_id", deliveryID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusAccepted) // 202 — delivery accepted and ticket created (FR-104).
	}
}

// runIngressTx opens a transaction, inserts the idempotency row, resolves
// the department slug, inserts the ticket, and backfills the delivery →
// ticket anchor. The transaction is rolled back on any error (including
// ErrDuplicateDelivery from the idempotency insert).
//
// Returns ErrDuplicateDelivery on a 23505 unique-violation (idempotency
// signal). Any other error is returned as-is; the caller maps it to 500.
func runIngressTx(
	ctx context.Context,
	pool *pgxpool.Pool,
	queries *store.Queries,
	connID, deliveryID string,
	draft TicketDraft,
	logger *slog.Logger,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()

	q := queries.WithTx(tx)

	// Step 9a: insert the idempotency row. A 23505 unique-violation means
	// this delivery was already processed; abort before any ticket insert
	// (plan decision 4, FR-202, M1 dedup-race safety).
	deliveryRowID, err := insertDelivery(ctx, q, connID, deliveryID)
	if err != nil {
		return err // includes ErrDuplicateDelivery
	}

	// Step 9b: resolve department slug → UUID.
	deptID, err := q.SelectDepartmentIDBySlug(ctx, draft.DepartmentSlug)
	if err != nil {
		return err
	}

	// Step 9c: insert the ticket with origin='ingress' + provenance metadata.
	// The emit_ticket_created trigger fires inside this INSERT and writes
	// the outbox row + the work.ticket.created.<dept>.todo notify (FR-101).
	acceptance := draft.Acceptance
	ticketRow, err := q.InsertIngressTicket(ctx, store.InsertIngressTicketParams{
		DepartmentID:       deptID,
		Objective:          draft.Objective,
		AcceptanceCriteria: &acceptance,
		IngressConnector:   connID,
		ExternalID:         draft.ExternalID,
		ExternalUrl:        draft.ExternalURL,
	})
	if err != nil {
		return err
	}

	// Step 9d: backfill ticket_id on the delivery row — links the idempotency
	// anchor to the created ticket (plan decision 4).
	if err := q.BackfillIngressDeliveryTicket(ctx, store.BackfillIngressDeliveryTicketParams{
		TicketID: ticketRow.ID,
		ID:       deliveryRowID,
	}); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true

	logger.Info("ingress: ticket created",
		"connector", connID,
		"delivery_id", deliveryID,
		"ticket_id", ticketRow.ID,
		"dept_slug", draft.DepartmentSlug,
	)
	return nil
}
