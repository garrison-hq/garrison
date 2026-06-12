// Package ingress implements the M10 connector framework: a supervisor-side
// subsystem that accepts inbound external events, verifies and filters them
// deterministically, dedups on a provider delivery key, and normalises each
// into a ticket row via the existing tickets INSERT path + emit_ticket_created
// trigger. The GitHub connector is the first concrete implementation
// (specs/022-m10-ingress-connectors). The framework deliberately adds no new
// spawn path — a verified, mapped delivery does exactly one thing: INSERT a
// ticket with origin='ingress' and connector provenance in tickets.metadata,
// in a single transaction that also INSERTs the idempotency row (FR-101,
// plan.md decision 1, 2, 11).
package ingress

import (
	"errors"
	"net/http"
)

// Connector is the per-provider plug-in surface. The framework owns
// signature verification, idempotency, the rate cap, and the ticket
// insert; a Connector supplies only provider-specific behaviour. The
// GitHub connector is the only implementation in M10-core (plan.md decision 2).
type Connector interface {
	// ID is the stable connector identity recorded in
	// ingress_deliveries.connector_id and tickets.metadata.ingress_connector.
	ID() string

	// EventType extracts the provider's event-type discriminator from
	// request headers (GitHub: X-GitHub-Event). ok=false means the header
	// is absent/malformed; the handler treats that as a discard-200.
	EventType(r *http.Request) (eventType string, ok bool)

	// Subscribed reports whether this connector processes eventType at all.
	// Unsubscribed types are discarded with 200 BEFORE signature verification
	// per SR6 step 1.
	Subscribed(eventType string) bool

	// DeliveryID extracts the provider idempotency key from headers
	// (GitHub: X-GitHub-Delivery). Empty means malformed delivery, 400.
	DeliveryID(r *http.Request) string

	// VerifySignature checks the provider HMAC over the raw body. Returns
	// ErrBadSignature (→ 401) on mismatch/missing (FR-300, SR1).
	VerifySignature(rawBody []byte, r *http.Request, secret []byte) error

	// Filter applies provider noise rules (bot sender, action subtype, ping)
	// to a parsed event. Returns FilterAccept / FilterDiscard; only
	// FilterAccept proceeds to mapping (FR-401, SR6 steps 3–4).
	Filter(eventType string, body []byte) (FilterDecision, error)

	// Map renders the accepted event into a TicketDraft using the
	// connector's configured event→department mapping + bounded templates
	// (FR-102, plan.md decision 10). Returns ErrNoMapping if eventType has
	// no configured route (treated as discard-200).
	Map(eventType string, body []byte) (TicketDraft, error)
}

// FilterDecision is the noise-filter verdict (plan.md decision 11).
type FilterDecision int

const (
	// FilterAccept means the event passes all noise rules and should proceed
	// to idempotency check, rate cap, and ticket insertion.
	FilterAccept FilterDecision = iota
	// FilterDiscard means the event is mechanical noise (bot sender,
	// non-actionable action subtype, ping). The handler returns 200 with no
	// ticket and no idempotency row.
	FilterDiscard
)

// TicketDraft is the normalised, provider-agnostic ticket the framework
// inserts. column_slug is always "todo" for M10-core (US1).
type TicketDraft struct {
	// DepartmentSlug is the target department resolved from the connector's
	// route configuration (plan.md decision 10).
	DepartmentSlug string
	// Objective is the rendered objective text (bounded template expansion
	// from the connector's ObjectiveTemplate, plan.md decision 10).
	Objective string
	// Acceptance is the rendered acceptance criteria text (bounded template
	// expansion from the connector's AcceptanceTemplate, plan.md decision 10).
	// Null issue/PR body renders to the "(no description provided)" literal
	// (FR-102, spike QS4).
	Acceptance string
	// ExternalID is the provider's stable entity identifier, stored in
	// tickets.metadata.external_id. For GitHub: the issue.id or
	// pull_request.id integer coerced to string (plan.md §GitHub connector).
	ExternalID string
	// ExternalURL is the human-readable link to the originating entity,
	// stored in tickets.metadata.external_url (e.g. issue.html_url).
	ExternalURL string
}

// Sentinel errors used by the framework and the GitHub connector. The HTTP
// status mapping is fixed by FR-401/SR6 (plan.md decision 11).
var (
	// ErrBadSignature is returned when the provider HMAC header is missing,
	// malformed, or does not match the computed digest. The handler returns
	// 401 and increments the per-connector rejection counter without writing
	// any row (FR-300, FR-301, SR1).
	ErrBadSignature = errors.New("ingress: bad or missing signature")

	// ErrMalformedDelivery is returned when a required header (e.g.
	// X-GitHub-Delivery) is absent or the payload is unparseable. The
	// handler returns 400.
	ErrMalformedDelivery = errors.New("ingress: malformed delivery")

	// ErrNoMapping is returned by Connector.Map when the event type has no
	// configured route. The handler returns 200 and discards the delivery
	// without writing any row.
	ErrNoMapping = errors.New("ingress: no route configured for event type")

	// ErrRateCapExceeded is the signal from the per-connector token bucket
	// when the delivery count exceeds the configured rate. The handler
	// returns 429 and writes a throttle_events evidence row via
	// throttle.FireIngressRateCap (FR-600, FR-601, FR-602).
	ErrRateCapExceeded = errors.New("ingress: rate cap exceeded")

	// ErrDuplicateDelivery is returned by insertDelivery when the unique
	// constraint on (connector_id, external_delivery_id) fires (Postgres
	// error code 23505). The handler returns 200 with no further side
	// effects (FR-201, FR-202, plan.md decision 3, 4).
	ErrDuplicateDelivery = errors.New("ingress: duplicate delivery")
)
