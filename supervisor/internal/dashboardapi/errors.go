// Package dashboardapi exposes the supervisor's HTTP surface for the
// dashboard process: cookie-validated reads of the Company.md MinIO
// object and the MemPalace recent-writes / recent-KG-triples surfaces.
//
// The package complements internal/health (the auth-free liveness
// probe). dashboardapi runs on a dedicated port (cfg.DashboardAPIPort,
// default 8081) so the auth-required /api/* routes are isolated from
// /health on cfg.HealthPort.
//
// File responsibilities:
//   - server.go       — Server lifecycle (NewServer, Serve, Shutdown)
//   - auth.go         — SessionValidator interface, sqlSessionValidator
//     impl, cookie-extracting middleware
//   - errors.go       — typed error response shape (FR-668)
//   - objstore_handler.go (T009) — GET / PUT /api/objstore/company-md
//   - mempalace_handler.go (T010) — GET /api/mempalace/recent-{writes,kg}
package dashboardapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ErrAuthExpired classifies an absent / invalid / expired session cookie.
// Surfaced via the auth middleware as 401 with errorResponse{Error:"AuthExpired"}.
var ErrAuthExpired = errors.New("dashboardapi: auth expired")

// errorResponse is the uniform JSON shape for typed errors per spec
// FR-668. PatternCategory is populated only on LeakScanFailed (FR-642).
// Message is optional context the dashboard surfaces inline; it never
// carries a verbatim secret substring (Rule 1 / SEC-4 carryover).
type errorResponse struct {
	Error           string `json:"error"`
	Message         string `json:"message,omitempty"`
	PatternCategory string `json:"pattern_category,omitempty"`
}

// writeErrorResponse writes a JSON error response with the given status
// code. PatternCategory and Message are optional; pass "" to omit.
//
// On JSON encoding failure (effectively impossible for fixed-shape
// strings), logs at the ERROR level via the package-supplied logger
// passed by the caller's site — but the body has already been started,
// so subsequent writes may be discarded by the client.
func writeErrorResponse(w http.ResponseWriter, status int, errKind, msg, patternCategory string, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := errorResponse{
		Error:           errKind,
		Message:         msg,
		PatternCategory: patternCategory,
	}
	if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
		logger.Error("dashboardapi: failed to write error response", "err", err)
	}
}
