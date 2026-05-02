package dashboardapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

// Deps wires the dashboardapi.Server into the supervisor process.
// Constructed once in cmd/supervisor/main.go (T011) after all
// dependencies are ready.
type Deps struct {
	Objstore         *objstore.Client
	Mempalace        *mempalace.QueryClient
	SessionValidator SessionValidator
	Logger           *slog.Logger
	// CompanyID is the single-company posture's pre-resolved companies
	// row id, captured at supervisor startup. Per plan §"Open questions
	// remaining for /garrison-tasks": pre-resolve at boot and pass
	// through to handlers, rather than per-request Postgres lookups.
	CompanyID string
}

// Server is the HTTP server lifecycle wrapper. Mirrors internal/health.
// Server's shape so cmd/supervisor can run them side-by-side in the
// existing errgroup.
type Server struct {
	httpServer    *http.Server
	shutdownGrace time.Duration
	logger        *slog.Logger
	mux           *http.ServeMux
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
		shutdownGrace: cfg.ShutdownGrace,
		logger:        logger,
		mux:           mux,
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

	if deps.Mempalace != nil {
		s.mux.Handle("/api/mempalace/recent-writes",
			auth(newRecentWritesHandler(deps.Mempalace, s.logger)))
		s.mux.Handle("/api/mempalace/recent-kg",
			auth(newRecentKGHandler(deps.Mempalace, s.logger)))
	}
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
