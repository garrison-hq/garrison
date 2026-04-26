package health

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
)

// PingTimeout bounds a single SELECT 1 issued by the /health handler
// (plan.md §"/health endpoint"). 500ms is the budget above which we prefer
// to answer 503 even if the query would eventually succeed — a health
// endpoint that blocks for seconds is itself a failure mode.
const PingTimeout = 500 * time.Millisecond

// Pinger is the minimal dependency the handler has on the database: a
// context-respecting connectivity check that returns nil on success. The
// production implementation wraps *pgxpool.Pool.Ping; tests substitute a
// fake so the handler can be exercised without a real Postgres.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Clock is the time source the handler consults for "has LastPollAt aged
// past 2 * PollInterval?" injected so tests can freeze time. Production
// passes realClock{}.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Server wraps net/http.Server with the fields NewServer wires up. Kept as
// a small struct so cmd/supervisor can call Serve + Shutdown without
// knowing internal details.
type Server struct {
	httpServer    *http.Server
	shutdownGrace time.Duration
}

// NewServer returns an http.Server bound to 0.0.0.0:cfg.HealthPort whose
// only route is GET /health. FR-016 + clarification Q2 pin the binding and
// the no-auth policy; the container runtime / Coolify routing is what keeps
// the endpoint off the public internet.
//
// The handler is constructed by NewHandler so the same logic can be
// exercised via httptest without a live TCP socket.
func NewServer(cfg *config.Config, state *State, pinger Pinger) *Server {
	mux := http.NewServeMux()
	mux.Handle("/health", NewHandler(state, pinger, cfg.PollInterval, realClock{}))
	return &Server{
		httpServer: &http.Server{
			Addr:              fmt.Sprintf("0.0.0.0:%d", cfg.HealthPort),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		shutdownGrace: cfg.ShutdownGrace,
	}
}

// Serve runs the HTTP server until ctx is cancelled, at which point it
// issues http.Server.Shutdown with the configured grace window. Returns
// nil on clean shutdown and a wrapped error on ListenAndServe failures
// other than http.ErrServerClosed.
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
			return fmt.Errorf("health: Shutdown: %w", err)
		}
		return nil
	}
}

// NewHandler is the pure HTTP handler factory — no net.Listen, no goroutines,
// just the logic that answers /health. Exported so server_test.go can wrap it
// with httptest without going through net/http.Server.
func NewHandler(state *State, pinger Pinger, pollInterval time.Duration, clock Clock) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), PingTimeout)
		defer cancel()

		now := clock.Now()
		pingErr := pinger.Ping(ctx)
		state.RecordPing(now, pingErr == nil)

		// Pipeline-freshness check: LastPollAt must not be older than
		// 2 * pollInterval. Zero time fails the freshness test, which is
		// the correct answer during the startup window before the first
		// poll has completed.
		pollOK := !state.LastPollAt().IsZero() && now.Sub(state.LastPollAt()) <= 2*pollInterval

		if pingErr == nil && pollOK {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		if pingErr != nil {
			_, _ = w.Write([]byte("db ping failed"))
			return
		}
		_, _ = w.Write([]byte("poll stale"))
	})
}
