package dashboardapi

// ingress_status_handler_test.go — T017 coverage top-up tests for
// newIngressStatusHandler (M10 T015 deliverable). The handler is already
// exercised end-to-end via the cookie-auth gate (RegisterDefaultRoutes calls
// auth(newIngressStatusHandler(...))), but a direct unit test gives the
// coverage probe the instrumented statements it needs.
//
// Per tasks.md T017 rubric: "top up tests if short — no production-code changes."

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestIngressStatusHandler_GET_NilCounter — GET with nil rejection counter
// returns HTTP 200 and bad_signature_rejections = 0.
func TestIngressStatusHandler_GET_NilCounter(t *testing.T) {
	h := newIngressStatusHandler(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/ingress/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}

	var resp ingressStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.BadSignatureRejections != 0 {
		t.Errorf("bad_signature_rejections = %d; want 0 when counter is nil", resp.BadSignatureRejections)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", ct)
	}
}

// TestIngressStatusHandler_GET_WithCounter — GET returns the current counter value.
func TestIngressStatusHandler_GET_WithCounter(t *testing.T) {
	var counter atomic.Int64
	counter.Add(7)

	h := newIngressStatusHandler(&counter, nil)
	req := httptest.NewRequest(http.MethodGet, "/ingress/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}

	var resp ingressStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.BadSignatureRejections != 7 {
		t.Errorf("bad_signature_rejections = %d; want 7", resp.BadSignatureRejections)
	}
}

// TestIngressStatusHandler_MethodNotAllowed — non-GET request returns 405.
// This exercises the method-guard branch in newIngressStatusHandler.
func TestIngressStatusHandler_MethodNotAllowed(t *testing.T) {
	h := newIngressStatusHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/ingress/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d; want 405 on POST", rec.Code)
	}
}

// TestIngressStatusHandler_CounterZero — newly minted atomic starts at zero.
func TestIngressStatusHandler_CounterZero(t *testing.T) {
	var counter atomic.Int64 // zero value

	h := newIngressStatusHandler(&counter, nil)
	req := httptest.NewRequest(http.MethodGet, "/ingress/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d; want 200", rec.Code)
	}

	var resp ingressStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.BadSignatureRejections != 0 {
		t.Errorf("bad_signature_rejections = %d; want 0 for zero-valued counter", resp.BadSignatureRejections)
	}
}
