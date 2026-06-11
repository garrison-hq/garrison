package dashboardapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/schedule"
)

// maxValidateBodyBytes bounds the POST /schedule/validate body read.
// A schedule expression is at most a few dozen bytes; 4 KiB leaves
// generous headroom for the JSON envelope while keeping the supervisor
// safe from unbounded bodies (same posture as objstore_handler.go's
// LimitReader).
const maxValidateBodyBytes = 4 << 10

// scheduleValidateRequest is the POST /schedule/validate body (plan §6:
// `{schedule_expr, mode?}`). Mode is accepted for forward-compat with
// the T014 Server-Action callers but is not validated here — this
// endpoint single-sources the *expression* grammar + min-interval check
// (decision 10); the DB-backed invariants (name uniqueness, department/
// role existence, mode enum) live in schedule.ValidateTask at write time.
type scheduleValidateRequest struct {
	ScheduleExpr string `json:"schedule_expr"`
	Mode         string `json:"mode,omitempty"`
}

// scheduleValidateResponse is the 200 success shape (plan §6:
// `{ok: true, next_fire_at, min_interval_ok: true}`). NextFireAt is the
// grammar's next slot strictly after "now", UTC, RFC 3339 — the
// dashboard never computes a fire time (decision 10).
type scheduleValidateResponse struct {
	OK            bool      `json:"ok"`
	NextFireAt    time.Time `json:"next_fire_at"`
	MinIntervalOK bool      `json:"min_interval_ok"`
}

// newScheduleValidateHandler returns the POST /schedule/validate
// handler. minInterval is the operator-tunable FR-404 floor
// (cfg.SchedMinInterval); `now` may be nil and defaults to time.Now
// (injectable so tests pin the next_fire_at computation).
//
// Rejections (grammar, sub-minimum interval) return 422 through
// writeErrorResponse with errKind "validation_failed" and the
// *ParseError / *ValidationError detail as the message — the same
// mapping the chat verb applies (decision 13).
func newScheduleValidateHandler(minInterval time.Duration, now func() time.Time, logger *slog.Logger) http.Handler {
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeErrorResponse(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "", "", logger)
			return
		}

		var req scheduleValidateRequest
		limited := io.LimitReader(r.Body, maxValidateBodyBytes)
		if err := json.NewDecoder(limited).Decode(&req); err != nil {
			writeErrorResponse(w, http.StatusBadRequest, "BadRequest", "request body is not valid JSON", "", logger)
			return
		}

		expr, err := schedule.Parse(req.ScheduleExpr)
		if err != nil {
			var pe *schedule.ParseError
			if errors.As(err, &pe) {
				writeErrorResponse(w, http.StatusUnprocessableEntity, "validation_failed", pe.Error(), "", logger)
				return
			}
			// Parse only returns *ParseError today; defensive against
			// future grammar revisions leaking a generic error.
			if logger != nil {
				logger.Error("dashboardapi: schedule.Parse returned non-ParseError", "err", err)
			}
			writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
			return
		}

		if got := expr.MinInterval(); got < minInterval {
			ve := &schedule.ValidationError{
				Field: "schedule_expr",
				Msg:   fmt.Sprintf("effective interval %s is below the minimum firing interval %s (FR-404)", got, minInterval),
			}
			writeErrorResponse(w, http.StatusUnprocessableEntity, "validation_failed", ve.Error(), "", logger)
			return
		}

		body := scheduleValidateResponse{
			OK:            true,
			NextFireAt:    expr.Next(now()),
			MinIntervalOK: true,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
			logger.Error("dashboardapi: write schedule/validate body", "err", err)
		}
	})
}
