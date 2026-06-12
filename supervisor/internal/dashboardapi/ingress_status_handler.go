package dashboardapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
)

// ingressStatusResponse is the JSON shape returned by GET /ingress/status.
// The bad-signature-rejection count is an in-process counter that resets to
// zero on supervisor restart — operators are aware of this limitation per
// FR-702 + plan resolution R3. The count is per-process and not persisted
// to any DB row (FR-301: rejected deliveries count toward nothing an attacker
// can inflate in Postgres).
type ingressStatusResponse struct {
	BadSignatureRejections int64 `json:"bad_signature_rejections"`
}

// newIngressStatusHandler returns the GET /ingress/status handler. The handler
// is cookie-auth-gated (identical auth middleware as all other dashboardapi
// routes) and returns 401 on unauthenticated requests.
//
// rejectionCounter may be nil (when ingress is disabled): the handler returns
// a count of 0 in that case — a nil pointer is a valid "disabled" signal.
//
// This endpoint lives on the dashboard-api port (8081), never on the public
// webhook port (8082), per plan resolution R3 and FR-702.
func newIngressStatusHandler(rejectionCounter *atomic.Int64, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "", "", logger)
			return
		}

		var count int64
		if rejectionCounter != nil {
			count = rejectionCounter.Load()
		}

		resp := ingressStatusResponse{
			BadSignatureRejections: count,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(resp); err != nil && logger != nil {
			logger.Error("dashboardapi: failed to write ingress status response", "err", err)
		}
	})
}
