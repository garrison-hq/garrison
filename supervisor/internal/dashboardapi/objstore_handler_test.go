package dashboardapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/leakscan"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

// fakeObjstore implements ObjstoreClient with canned outcomes.
type fakeObjstore struct {
	getContent []byte
	getETag    string
	getErr     error

	putETag string
	putErr  error

	// captured on PUT
	putContent   []byte
	putIfMatch   string
	wasGetCalled bool
	wasPutCalled bool
}

func (f *fakeObjstore) GetCompanyMD(_ context.Context) ([]byte, string, error) {
	f.wasGetCalled = true
	return f.getContent, f.getETag, f.getErr
}

func (f *fakeObjstore) PutCompanyMD(_ context.Context, content []byte, ifMatch string) (string, error) {
	f.wasPutCalled = true
	f.putContent = append([]byte(nil), content...)
	f.putIfMatch = ifMatch
	return f.putETag, f.putErr
}

// TestObjstoreHandler_GetReturnsContent — happy path: client returns
// content + etag → 200 with the canonical JSON shape.
func TestObjstoreHandler_GetReturnsContent(t *testing.T) {
	fake := &fakeObjstore{getContent: []byte("# Garrison\n\nv1"), getETag: `"abc123"`}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", rec.Code)
	}
	var body getResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if body.Content != "# Garrison\n\nv1" {
		t.Errorf("Content=%q; want '# Garrison\\n\\nv1'", body.Content)
	}
	if body.ETag == nil || *body.ETag != `"abc123"` {
		t.Errorf("ETag=%v; want '\"abc123\"'", body.ETag)
	}
}

// TestObjstoreHandler_GetReturnsEmptyForMissing — FR-624: client
// returns (nil, "", nil) → 200 with content="" and etag=null.
func TestObjstoreHandler_GetReturnsEmptyForMissing(t *testing.T) {
	fake := &fakeObjstore{} // zero value: nil content, "" etag, nil err
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", rec.Code)
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, `"etag":null`) {
		t.Errorf("expected etag=null; body=%s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"content":""`) {
		t.Errorf("expected content=''; body=%s", bodyStr)
	}
}

// TestObjstoreHandler_GetSurfacesUnreachable — client returns
// ErrMinIOUnreachable → 503 with typed error.
func TestObjstoreHandler_GetSurfacesUnreachable(t *testing.T) {
	fake := &fakeObjstore{getErr: objstore.ErrMinIOUnreachable}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d; want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"MinIOUnreachable"`) {
		t.Errorf("body missing MinIOUnreachable: %s", rec.Body.String())
	}
}

// TestObjstoreHandler_GetSurfacesAuthFailed — client returns
// ErrMinIOAuthFailed → 500 with MinIOAuthFailed.
func TestObjstoreHandler_GetSurfacesAuthFailed(t *testing.T) {
	fake := &fakeObjstore{getErr: objstore.ErrMinIOAuthFailed}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status=%d; want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"MinIOAuthFailed"`) {
		t.Errorf("body missing MinIOAuthFailed: %s", rec.Body.String())
	}
}

// TestObjstoreHandler_PutHappyPath — clean content + valid etag → 200
// with new etag.
func TestObjstoreHandler_PutHappyPath(t *testing.T) {
	fake := &fakeObjstore{putETag: `"new-etag"`}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/objstore/company-md", bytes.NewReader([]byte("# v2")))
	req.Header.Set("If-Match", `"old-etag"`)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !fake.wasPutCalled {
		t.Errorf("expected PutCompanyMD called")
	}
	if fake.putIfMatch != `"old-etag"` {
		t.Errorf("If-Match=%q; want '\"old-etag\"'", fake.putIfMatch)
	}
	if string(fake.putContent) != "# v2" {
		t.Errorf("body=%q; want '# v2'", fake.putContent)
	}
	var body putResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ETag != `"new-etag"` {
		t.Errorf("ETag=%q; want '\"new-etag\"'", body.ETag)
	}
}

// TestObjstoreHandler_PutRejectsLeakScan — LeakScanError is surfaced as
// 422 with the matched pattern category populated; the matched substring
// is NOT present anywhere in the response (Rule 1 / SEC-4).
func TestObjstoreHandler_PutRejectsLeakScan(t *testing.T) {
	const matchedSubstring = "sk-1234567890abcdef1234567890abcd"
	fake := &fakeObjstore{
		putErr: &objstore.LeakScanError{Category: leakscan.CategorySKPrefix},
	}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/objstore/company-md", bytes.NewReader([]byte("body with "+matchedSubstring)))
	req.Header.Set("If-Match", `"old-etag"`)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d; want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"LeakScanFailed"`) {
		t.Errorf("body missing LeakScanFailed: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"pattern_category":"`+string(leakscan.CategorySKPrefix)+`"`) {
		t.Errorf("body missing pattern_category: %s", rec.Body.String())
	}
	// Rule 1: response body must NOT contain the matched substring.
	if strings.Contains(rec.Body.String(), matchedSubstring) {
		t.Errorf("body must not echo matched substring: %s", rec.Body.String())
	}
}

// TestObjstoreHandler_PutRejectsTooLarge — ErrTooLarge → 413.
func TestObjstoreHandler_PutRejectsTooLarge(t *testing.T) {
	fake := &fakeObjstore{putErr: objstore.ErrTooLarge}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/objstore/company-md", bytes.NewReader([]byte("body")))
	req.Header.Set("If-Match", `"old-etag"`)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status=%d; want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"TooLarge"`) {
		t.Errorf("body missing TooLarge: %s", rec.Body.String())
	}
}

// TestObjstoreHandler_PutRejectsStale — ErrStale → 412.
func TestObjstoreHandler_PutRejectsStale(t *testing.T) {
	fake := &fakeObjstore{putErr: objstore.ErrStale}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/objstore/company-md", bytes.NewReader([]byte("body")))
	req.Header.Set("If-Match", `"stale-etag"`)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("status=%d; want 412", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"Stale"`) {
		t.Errorf("body missing Stale: %s", rec.Body.String())
	}
}

// TestObjstoreHandler_PutForwardsEmptyIfMatch — FR-624 first-save:
// the dashboard's saveCompanyMD Server Action sends If-Match: ” on
// the first save against an empty-state load. The handler MUST pass
// the empty string through to objstore.Client.PutCompanyMD (which
// handles the empty-vs-missing combination correctly) instead of
// rejecting at the handler layer with 400 MissingIfMatch.
func TestObjstoreHandler_PutForwardsEmptyIfMatch(t *testing.T) {
	fake := &fakeObjstore{putETag: `"first-etag"`}
	h := newObjstoreHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/objstore/company-md", bytes.NewReader([]byte("# v1")))
	// Deliberately no If-Match header — first-save signal.
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status=%d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !fake.wasPutCalled {
		t.Errorf("PutCompanyMD must be called even with empty If-Match")
	}
	if fake.putIfMatch != "" {
		t.Errorf("forwarded If-Match=%q; want empty string (FR-624)", fake.putIfMatch)
	}
}
