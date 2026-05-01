package dashboardapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestErrors_JSONShape verifies the errorResponse struct serialises to
// the exact spec FR-668 shape: { "error": "<KindString>", "message"?:
// "<...>", "pattern_category"?: "<...>" }. Optional fields are omitted
// when empty.
func TestErrors_JSONShape(t *testing.T) {
	t.Run("OnlyErrorField", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeErrorResponse(rec, 401, "AuthExpired", "", "", nil)

		body := strings.TrimSpace(rec.Body.String())
		// Must NOT contain the optional keys.
		if strings.Contains(body, "message") {
			t.Errorf("expected message field omitted; body=%s", body)
		}
		if strings.Contains(body, "pattern_category") {
			t.Errorf("expected pattern_category field omitted; body=%s", body)
		}
		// Must contain the canonical error kind.
		if !strings.Contains(body, `"error":"AuthExpired"`) {
			t.Errorf("expected error kind; body=%s", body)
		}
		// Decode and verify shape.
		var got errorResponse
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Error != "AuthExpired" {
			t.Errorf("Error=%q; want AuthExpired", got.Error)
		}
		if got.Message != "" || got.PatternCategory != "" {
			t.Errorf("expected empty optional fields; got %+v", got)
		}
		if rec.Code != 401 {
			t.Errorf("status=%d; want 401", rec.Code)
		}
		if got, want := rec.Header().Get("Content-Type"), "application/json"; got != want {
			t.Errorf("Content-Type=%q; want %q", got, want)
		}
	})

	t.Run("WithPatternCategoryAndMessage", func(t *testing.T) {
		rec := httptest.NewRecorder()
		writeErrorResponse(rec, 422, "LeakScanFailed", "secret detected", "sk-prefix", nil)

		body := strings.TrimSpace(rec.Body.String())
		var got errorResponse
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Error != "LeakScanFailed" {
			t.Errorf("Error=%q; want LeakScanFailed", got.Error)
		}
		if got.Message != "secret detected" {
			t.Errorf("Message=%q; want 'secret detected'", got.Message)
		}
		if got.PatternCategory != "sk-prefix" {
			t.Errorf("PatternCategory=%q; want sk-prefix", got.PatternCategory)
		}
		if rec.Code != 422 {
			t.Errorf("status=%d; want 422", rec.Code)
		}
	})
}
