package ingress

import (
	"sync/atomic"

	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps wires the ingress.Server into the supervisor process. Constructed
// once in cmd/supervisor/main.go after all boot dependencies are ready,
// mirroring the dashboardapi.Server pattern (plan.md decision 6, 9, 12;
// T008).
//
// The vault client seam allows the integration test suite to inject a
// failing fetcher (TestIngress_VaultUnavailable_FailsClosed, FR-302).
//
// CompanyID is resolved once at supervisor boot via
//
//	SELECT id FROM companies LIMIT 1
//
// (the M6 pattern — single-company posture, plan §rate cap). It is passed
// to throttle.FireIngressRateCap on a rate-cap breach so no per-request
// company query is needed (plan R3).
type Deps struct {
	// Pool is the pgxpool used to Begin each webhook transaction and
	// to write throttle evidence (not inside the delivery tx — the cap
	// fires before any delivery row is created, FR-602).
	Pool *pgxpool.Pool

	// Queries is the supervisor sqlc query set. Pool-level (not tx-bound)
	// for throttle writes; the handler derives a tx-bound copy via
	// Queries.WithTx for the delivery + ticket inserts.
	Queries *store.Queries

	// VaultClient is the seam used to fetch the webhook secret at
	// server construction. A nil VaultClient causes NewServer to return
	// an error when GitHub ingress is enabled (fail-closed per FR-302).
	// In production this is the *vault.Client returned by buildVaultClient.
	VaultClient vault.Fetcher

	// CustomerID is the pre-resolved companies row id for the
	// single-company alpha posture. Passed through to
	// throttle.FireIngressRateCap so the handler avoids a per-request DB
	// lookup (plan §rate cap, plan R3).
	CustomerID pgtype.UUID

	// RejectionCounter is incremented on every 401 (bad/missing
	// signature) response without writing a DB row (FR-301, plan R3).
	// Exposed via GET /ingress/status on the dashboard-api port (T015).
	// Must be non-nil before NewServer is called. cmd/supervisor allocates
	// it and holds the pointer for the /ingress/status handler.
	RejectionCounter *atomic.Int64
}
