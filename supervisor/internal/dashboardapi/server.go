package dashboardapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

// Deps wires the dashboardapi.Server into the supervisor process.
// Constructed once in cmd/supervisor/main.go (T011) after all
// dependencies are ready.
type Deps struct {
	Objstore         *objstore.Client
	Mempalace        *mempalace.QueryClient
	SessionValidator SessionValidator
	Logger           *slog.Logger
	// Queries is the supervisor's sqlc query set (M9 review #4): the
	// /schedule/validate handler needs it for the full-body
	// schedule.ValidateTask path (role existence + name uniqueness).
	// nil keeps expression-only validation working (older tests).
	Queries *store.Queries
	// CompanyID is the single-company posture's pre-resolved companies
	// row id, captured at supervisor startup. Per plan §"Open questions
	// remaining for /garrison-tasks": pre-resolve at boot and pass
	// through to handlers, rather than per-request Postgres lookups.
	CompanyID string
	// IngressRejectionCounter is the M10 ingress bad-signature rejection
	// counter (FR-301, FR-702, plan resolution R3). The atomic is owned
	// by the ingress.Server and shared here so the GET /ingress/status
	// handler can expose it over the cookie-auth dashboard-api port (8081)
	// without database writes for each rejection. May be nil when ingress
	// is disabled; the handler returns 0 in that case.
	IngressRejectionCounter *atomic.Int64
}

// Server is the HTTP server lifecycle wrapper. Mirrors internal/health.
// Server's shape so cmd/supervisor can run them side-by-side in the
// existing errgroup.
type Server struct {
	httpServer    *http.Server
	shutdownGrace time.Duration
	logger        *slog.Logger
	mux           *http.ServeMux
	// schedMinInterval is the FR-404 minimum firing interval
	// (cfg.SchedMinInterval), captured at construction for the M9
	// POST /schedule/validate handler.
	schedMinInterval time.Duration
}

// NewServer wires the auth middleware, but leaves route registration
// to T009 (objstore handler) and T010 (mempalace handlers). Those
// tasks call Server.RegisterRoutes (or, equivalently, register against
// the exported Mux). The server is nominally complete after T010; this
// task ships only the lifecycle + middleware skeleton.
func NewServer(cfg *config.Config, deps Deps) *Server {
	mux := http.NewServeMux()
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf("0.0.0.0:%d", cfg.DashboardAPIPort),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		shutdownGrace:    cfg.ShutdownGrace,
		logger:           logger,
		mux:              mux,
		schedMinInterval: cfg.SchedMinInterval,
	}
}

// Mux exposes the underlying ServeMux so test code can register
// supplementary handlers without breaking encapsulation. Production
// route-registration lives in RegisterDefaultRoutes.
func (s *Server) Mux() *http.ServeMux { return s.mux }

// AuthMiddleware returns the authentication middleware bound to the
// given validator. Used by RegisterDefaultRoutes; exported for test
// code that wants to wrap a custom handler with the same auth rules.
func (s *Server) AuthMiddleware(validator SessionValidator) func(http.Handler) http.Handler {
	return newAuthMiddleware(validator, s.logger)
}

// NewSQLSessionValidator constructs the production SessionValidator
// from a SessionRowQuery seam. cmd/supervisor wires the seam to a
// closure over pgxpool.Pool.QueryRow against the dashboard's
// better-auth `sessions` table. `now` may be nil; defaults to time.Now.
func NewSQLSessionValidator(queryRow SessionRowQuery, now func() time.Time) SessionValidator {
	return newSQLSessionValidator(queryRow, now)
}

// RegisterDefaultRoutes wires the M5.4 route set against the Server's
// Mux behind the auth middleware. Called once from cmd/supervisor at
// boot. The supplied Deps must have non-nil Objstore + SessionValidator;
// Mempalace may be nil (production wires it; tests can omit). Returns
// an error only when Deps are insufficiently populated.
func (s *Server) RegisterDefaultRoutes(deps Deps) error {
	if deps.Objstore == nil {
		return errMissingDep("Objstore")
	}
	if deps.SessionValidator == nil {
		return errMissingDep("SessionValidator")
	}
	auth := s.AuthMiddleware(deps.SessionValidator)

	s.mux.Handle("/api/objstore/company-md",
		auth(newObjstoreHandler(deps.Objstore, s.logger)))

	// M9 T013: expression validation single-sources in Go (plan
	// decision 10) — the dashboard's Server Actions call this endpoint
	// for grammar + next-fire computation; no TS date-math mirror.
	// Review #4: deps.Queries enables the full-body ValidateTask path
	// (role existence + duplicate-live-name, typed field errors).
	s.mux.Handle("/schedule/validate",
		auth(newScheduleValidateHandler(deps.Queries, s.schedMinInterval, nil, s.logger)))

	if deps.Mempalace != nil {
		s.mux.Handle("/api/mempalace/recent-writes",
			auth(newRecentWritesHandler(deps.Mempalace, s.logger)))
		s.mux.Handle("/api/mempalace/recent-kg",
			auth(newRecentKGHandler(deps.Mempalace, s.logger)))
	}

	// M10 T015 — GET /ingress/status: per-connector bad-signature rejection
	// count from the in-process atomic counter (FR-702, plan resolution R3).
	// Cookie-auth gated like all other dashboardapi routes; 401 on
	// unauthenticated requests.
	s.mux.Handle("/ingress/status",
		auth(newIngressStatusHandler(deps.IngressRejectionCounter, s.logger)))

	return nil
}

type missingDepError struct{ name string }

func (e *missingDepError) Error() string {
	return "dashboardapi: required Deps." + e.name + " is nil"
}

func errMissingDep(name string) error { return &missingDepError{name: name} }

// Serve runs the HTTP server until ctx is cancelled, then issues
// http.Server.Shutdown with the configured grace window. Mirrors
// internal/health.Server.Serve byte-for-byte (concurrency rule 6:
// shutdown context derived via context.WithoutCancel + grace).
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownGrace)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("dashboardapi: Shutdown: %w", err)
		}
		return nil
	}
}
