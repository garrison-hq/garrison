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
	"github.com/garrison-hq/garrison/supervisor/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// maxValidateBodyBytes bounds the POST /schedule/validate body read.
// The full-body shape (M9 review #4) carries the two templates, which
// the dashboard caps at form-field sizes; 64 KiB leaves generous
// headroom while keeping the supervisor safe from unbounded bodies
// (same posture as objstore_handler.go's LimitReader).
const maxValidateBodyBytes = 64 << 10

// scheduleValidateRequest is the POST /schedule/validate body. The
// original M9 shape was `{schedule_expr, mode?}` — expression grammar +
// min-interval only. Review #4 widened it: when any of the OPTIONAL
// task-identity fields (name / department_id / role_slug /
// objective_template / acceptance_criteria_template) is present, the
// handler runs the full schedule.ValidateTask — the same FR-105 path
// the chat verb uses — so the dashboard's create/edit also catch
// unknown roles and duplicate live names BEFORE the write tx.
// Expression-only bodies keep the original behavior (backward
// compatible; the resume action still sends just the expression).
type scheduleValidateRequest struct {
	ScheduleExpr string `json:"schedule_expr"`
	Mode         string `json:"mode,omitempty"`

	// Optional full-validation fields (M9 review #4).
	Name                       string `json:"name,omitempty"`
	DepartmentID               string `json:"department_id,omitempty"`
	RoleSlug                   string `json:"role_slug,omitempty"`
	ObjectiveTemplate          string `json:"objective_template,omitempty"`
	AcceptanceCriteriaTemplate string `json:"acceptance_criteria_template,omitempty"`
}

// fullValidation reports whether the body carries any task-identity
// field — the switch between expression-only and full ValidateTask.
// Mode alone does NOT trigger full validation: the pre-review T014
// callers already sent `{schedule_expr, mode}` and expect the
// expression-only contract.
func (r scheduleValidateRequest) fullValidation() bool {
	return r.Name != "" || r.DepartmentID != "" || r.RoleSlug != "" ||
		r.ObjectiveTemplate != "" || r.AcceptanceCriteriaTemplate != ""
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

// scheduleFieldErrorResponse is the 422 shape for full-body validation
// rejections: the errorResponse envelope plus the typed field, so the
// dashboard can attach the message to the offending form input.
type scheduleFieldErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// writeScheduleFieldError writes the 422 validation_failed body with
// the typed field detail (M9 review #4).
func writeScheduleFieldError(w http.ResponseWriter, msg, field string, logger *slog.Logger) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnprocessableEntity)
	body := scheduleFieldErrorResponse{Error: "validation_failed", Message: msg, Field: field}
	if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
		logger.Error("dashboardapi: failed to write schedule field error", "err", err)
	}
}

// newScheduleValidateHandler returns the POST /schedule/validate
// handler. queries is the real *store.Queries (M9 review #4 — required
// for the full-body ValidateTask path; nil keeps expression-only bodies
// working and 500s full bodies, the construction the unit tests use).
// minInterval is the operator-tunable FR-404 floor
// (cfg.SchedMinInterval); `now` may be nil and defaults to time.Now
// (injectable so tests pin the next_fire_at computation).
//
// Rejections (grammar, sub-minimum interval, and — on full bodies —
// every FR-105 invariant: unknown mode, empty templates, duplicate live
// name, missing department, missing role) return 422 with errKind
// "validation_failed", the *ParseError / *ValidationError detail as the
// message, and (full-body path) the typed field — the same mapping the
// chat verb applies (decision 13).
func newScheduleValidateHandler(queries *store.Queries, minInterval time.Duration, now func() time.Time, logger *slog.Logger) http.Handler {
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

		if req.fullValidation() {
			handleFullValidate(w, r, queries, minInterval, now, req, logger)
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

		writeScheduleValidateOK(w, expr.Next(now()), logger)
	})
}

// handleFullValidate runs the full schedule.ValidateTask path for
// bodies carrying task-identity fields (M9 review #4): one shared
// FR-105 validator for both authoring surfaces, so the dashboard's
// create/edit reject unknown roles and duplicate live names with typed
// field errors instead of deferring to the INSERT's index collision.
func handleFullValidate(
	w http.ResponseWriter,
	r *http.Request,
	queries *store.Queries,
	minInterval time.Duration,
	now func() time.Time,
	req scheduleValidateRequest,
	logger *slog.Logger,
) {
	if queries == nil {
		if logger != nil {
			logger.Error("dashboardapi: full-body /schedule/validate without wired queries")
		}
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
		return
	}
	var deptID pgtype.UUID
	if err := deptID.Scan(req.DepartmentID); err != nil {
		writeScheduleFieldError(w, fmt.Sprintf("invalid department_id: %q is not a UUID", req.DepartmentID), "department_id", logger)
		return
	}

	next, err := schedule.ValidateTask(r.Context(), queries, minInterval, now().UTC(), schedule.ValidationInput{
		Name:               req.Name,
		RoleSlug:           req.RoleSlug,
		ScheduleExpr:       req.ScheduleExpr,
		ObjectiveTemplate:  req.ObjectiveTemplate,
		AcceptanceTemplate: req.AcceptanceCriteriaTemplate,
		DepartmentID:       deptID,
		Mode:               req.Mode,
	})
	if err != nil {
		var ve *schedule.ValidationError
		if errors.As(err, &ve) {
			writeScheduleFieldError(w, ve.Error(), ve.Field, logger)
			return
		}
		if logger != nil {
			logger.Error("dashboardapi: schedule.ValidateTask failed", "err", err)
		}
		writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
		return
	}
	writeScheduleValidateOK(w, next, logger)
}

// writeScheduleValidateOK writes the shared 200 success body.
func writeScheduleValidateOK(w http.ResponseWriter, next time.Time, logger *slog.Logger) {
	body := scheduleValidateResponse{
		OK:            true,
		NextFireAt:    next,
		MinIntervalOK: true,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(body); err != nil && logger != nil {
		logger.Error("dashboardapi: write schedule/validate body", "err", err)
	}
}
