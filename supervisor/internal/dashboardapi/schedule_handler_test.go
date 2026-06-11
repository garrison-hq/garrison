package dashboardapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedNow pins the next_fire_at computation: Wednesday 2026-06-10
// 12:00:00 UTC.
var fixedNow = time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

func scheduleValidatePOST(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/schedule/validate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// TestScheduleValidateAcceptsGrammar — each of the three FR-103 grammar
// forms returns 200 {ok:true, next_fire_at, min_interval_ok:true} with
// the Go-computed next slot (decision 10: the dashboard never computes
// a fire time).
func TestScheduleValidateAcceptsGrammar(t *testing.T) {
	h := newScheduleValidateHandler(nil, 15*time.Minute, func() time.Time { return fixedNow }, nil)

	cases := []struct {
		expr     string
		wantNext time.Time
	}{
		// daily@06:00 — 06:00 already passed at fixedNow 12:00 → tomorrow.
		{"daily@06:00", time.Date(2026, 6, 11, 6, 0, 0, 0, time.UTC)},
		// weekly@mon@09:30 — fixedNow is Wednesday → next Monday.
		{"weekly@mon@09:30", time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC)},
		// every@30m — strictly after now by the interval.
		{"every@30m", fixedNow.Add(30 * time.Minute)},
	}

	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			rec := scheduleValidatePOST(t, h, `{"schedule_expr":"`+tc.expr+`"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d; want 200; body=%s", rec.Code, rec.Body.String())
			}
			var body scheduleValidateResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v: %s", err, rec.Body.String())
			}
			if !body.OK {
				t.Errorf("ok=false; want true")
			}
			if !body.MinIntervalOK {
				t.Errorf("min_interval_ok=false; want true")
			}
			if !body.NextFireAt.Equal(tc.wantNext) {
				t.Errorf("next_fire_at=%v; want %v", body.NextFireAt, tc.wantNext)
			}
		})
	}
}

// TestScheduleValidateRejects422WithDetail — grammar rejections and
// sub-minimum intervals both surface as 422 with errKind
// "validation_failed" and the *ParseError / *ValidationError detail as
// the message (plan §6 / decision 13).
func TestScheduleValidateRejects422WithDetail(t *testing.T) {
	h := newScheduleValidateHandler(nil, 15*time.Minute, func() time.Time { return fixedNow }, nil)

	cases := []struct {
		name       string
		body       string
		wantDetail string
	}{
		{
			name:       "bad grammar",
			body:       `{"schedule_expr":"0 6 * * *"}`,
			wantDetail: "want daily@HH:MM, weekly@{mon..sun}@HH:MM, or every@<N>{m|h}",
		},
		{
			name:       "empty expression",
			body:       `{"schedule_expr":""}`,
			wantDetail: "want daily@HH:MM, weekly@{mon..sun}@HH:MM, or every@<N>{m|h}",
		},
		{
			name:       "sub-minimum interval",
			body:       `{"schedule_expr":"every@5m"}`,
			wantDetail: "below the minimum firing interval 15m0s (FR-404)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := scheduleValidatePOST(t, h, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d; want 422; body=%s", rec.Code, rec.Body.String())
			}
			var body errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v: %s", err, rec.Body.String())
			}
			if body.Error != "validation_failed" {
				t.Errorf("error=%q; want \"validation_failed\"", body.Error)
			}
			if !strings.Contains(body.Message, tc.wantDetail) {
				t.Errorf("message=%q; want substring %q", body.Message, tc.wantDetail)
			}
		})
	}
}

// TestScheduleValidateRequiresAuth — the route is registered behind the
// same auth middleware as objstore_handler.go: no session cookie → 401
// AuthExpired, and the validation handler never runs.
func TestScheduleValidateRequiresAuth(t *testing.T) {
	mw := newAuthMiddleware(fakeValidator{err: ErrAuthExpired}, nil)
	h := mw(newScheduleValidateHandler(nil, 15*time.Minute, func() time.Time { return fixedNow }, nil))

	t.Run("no cookie", func(t *testing.T) {
		rec := scheduleValidatePOST(t, h, `{"schedule_expr":"daily@06:00"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d; want 401; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"error":"AuthExpired"`) {
			t.Errorf("body missing AuthExpired: %s", rec.Body.String())
		}
	})

	t.Run("invalid cookie", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/schedule/validate", strings.NewReader(`{"schedule_expr":"daily@06:00"}`))
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "expired-token"})
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status=%d; want 401; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"error":"AuthExpired"`) {
			t.Errorf("body missing AuthExpired: %s", rec.Body.String())
		}
	})
}

// TestScheduleValidateMethodNotAllowed — non-POST methods are refused
// with 405 + an Allow header (same posture as objstore_handler.go).
func TestScheduleValidateMethodNotAllowed(t *testing.T) {
	h := newScheduleValidateHandler(nil, 15*time.Minute, func() time.Time { return fixedNow }, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/schedule/validate", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d; want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Errorf("Allow = %q; want POST", allow)
	}
}

// TestScheduleValidateRejectsMalformedJSON — a non-JSON body returns
// 400 BadRequest, not a 422 validation rejection (the body never
// reached the grammar).
func TestScheduleValidateRejectsMalformedJSON(t *testing.T) {
	h := newScheduleValidateHandler(nil, 15*time.Minute, func() time.Time { return fixedNow }, nil)
	rec := scheduleValidatePOST(t, h, `{"schedule_expr": "daily@09:00"`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not valid JSON") {
		t.Errorf("body = %s; want the JSON-decode detail", rec.Body.String())
	}
}

// TestScheduleValidateFullBodyRequiresQueries (M9 review #4): a body
// carrying task-identity fields needs the wired *store.Queries for the
// ValidateTask path; the nil-queries construction (this unit suite's
// shape) 500s rather than silently downgrading to expression-only
// validation. Expression-only bodies keep working with nil queries —
// every other test in this file pins that.
func TestScheduleValidateFullBodyRequiresQueries(t *testing.T) {
	h := newScheduleValidateHandler(nil, 15*time.Minute, func() time.Time { return fixedNow }, nil)
	rec := scheduleValidatePOST(t, h,
		`{"schedule_expr":"daily@06:00","mode":"ticket","name":"digest","department_id":"00000000-0000-0000-0000-000000000001","role_slug":"engineer","objective_template":"o","acceptance_criteria_template":"a"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d; want 500 (full body without queries); body=%s", rec.Code, rec.Body.String())
	}
}

// TestScheduleValidateDefaultsNowFunc — a nil `now` falls back to
// time.Now; the computed next_fire_at is strictly future.
func TestScheduleValidateDefaultsNowFunc(t *testing.T) {
	h := newScheduleValidateHandler(nil, 15*time.Minute, nil, nil)
	before := time.Now().UTC()
	rec := scheduleValidatePOST(t, h, `{"schedule_expr":"every@30m"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body scheduleValidateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.NextFireAt.After(before) {
		t.Errorf("next_fire_at = %s; want strictly after %s", body.NextFireAt, before)
	}
}
