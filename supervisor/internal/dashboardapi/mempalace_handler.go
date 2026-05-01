package dashboardapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
)

// MempalaceQueryClient is the seam newRecentWritesHandler /
// newRecentKGHandler use for tests. Production wires
// *mempalace.QueryClient.
type MempalaceQueryClient interface {
	RecentDrawers(ctx context.Context, limit int) ([]mempalace.DrawerEntry, error)
	RecentKGTriples(ctx context.Context, limit int) ([]mempalace.KGTriple, error)
}

// recentWritesResponse is the success-path body for /api/mempalace/recent-writes.
type recentWritesResponse struct {
	Writes []mempalace.DrawerEntry `json:"writes"`
}

// recentKGResponse is the success-path body for /api/mempalace/recent-kg.
type recentKGResponse struct {
	Facts []mempalace.KGTriple `json:"facts"`
}

const (
	defaultMempalaceLimit = 30
	maxMempalaceLimit     = 100
)

// parseLimit reads the "?limit=N" query param, applies the FR-685
// default (30) and clamp (≤100). Negative / zero / non-numeric values
// fall back to the default. Values > maxMempalaceLimit clamp to
// maxMempalaceLimit.
func parseLimit(r *http.Request) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return defaultMempalaceLimit
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return defaultMempalaceLimit
	}
	if n > maxMempalaceLimit {
		return maxMempalaceLimit
	}
	return n
}

// newRecentWritesHandler returns the GET handler for
// /api/mempalace/recent-writes. Read-only; SEC-3 carryover keeps the
// proxy strictly read-only — write paths are agent-only via the M2.2
// Client.
func newRecentWritesHandler(client MempalaceQueryClient, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "", "", logger)
			return
		}
		limit := parseLimit(r)
		writes, err := client.RecentDrawers(r.Context(), limit)
		if err != nil {
			if errors.Is(err, mempalace.ErrSidecarUnreachable) {
				writeErrorResponse(w, http.StatusServiceUnavailable, "MempalaceUnreachable", "", "", logger)
				return
			}
			if logger != nil {
				logger.Error("dashboardapi: RecentDrawers failed", "err", err)
			}
			writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
			return
		}
		if writes == nil {
			writes = []mempalace.DrawerEntry{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(recentWritesResponse{Writes: writes}); err != nil && logger != nil {
			logger.Error("dashboardapi: write recent-writes body", "err", err)
		}
	})
}

// newRecentKGHandler returns the GET handler for
// /api/mempalace/recent-kg.
func newRecentKGHandler(client MempalaceQueryClient, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "", "", logger)
			return
		}
		limit := parseLimit(r)
		facts, err := client.RecentKGTriples(r.Context(), limit)
		if err != nil {
			if errors.Is(err, mempalace.ErrSidecarUnreachable) {
				writeErrorResponse(w, http.StatusServiceUnavailable, "MempalaceUnreachable", "", "", logger)
				return
			}
			if logger != nil {
				logger.Error("dashboardapi: RecentKGTriples failed", "err", err)
			}
			writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
			return
		}
		if facts == nil {
			facts = []mempalace.KGTriple{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(recentKGResponse{Facts: facts}); err != nil && logger != nil {
			logger.Error("dashboardapi: write recent-kg body", "err", err)
		}
	})
}
