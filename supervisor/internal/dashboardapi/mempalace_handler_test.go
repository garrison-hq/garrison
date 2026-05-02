package dashboardapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/mempalace"
)

// fakeMempalace implements MempalaceQueryClient with canned outcomes
// and captures the limit argument for assertion.
type fakeMempalace struct {
	drawers       []mempalace.DrawerEntry
	drawersErr    error
	triples       []mempalace.KGTriple
	triplesErr    error
	gotDrawerLim  int
	gotTripleLim  int
	drawersCalled bool
	triplesCalled bool
}

func (f *fakeMempalace) RecentDrawers(_ context.Context, limit int) ([]mempalace.DrawerEntry, error) {
	f.drawersCalled = true
	f.gotDrawerLim = limit
	return f.drawers, f.drawersErr
}

func (f *fakeMempalace) RecentKGTriples(_ context.Context, limit int) ([]mempalace.KGTriple, error) {
	f.triplesCalled = true
	f.gotTripleLim = limit
	return f.triples, f.triplesErr
}

// TestMempalaceHandler_RecentWritesProxies — fake returns 5 rows → 200
// with the expected JSON shape under the "writes" key.
func TestMempalaceHandler_RecentWritesProxies(t *testing.T) {
	at := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	fake := &fakeMempalace{
		drawers: []mempalace.DrawerEntry{
			{ID: "d1", DrawerName: "n1", RoomName: "r1", WingName: "w1", WrittenAt: at, BodyPreview: "p1"},
			{ID: "d2", DrawerName: "n2", RoomName: "r2", WingName: "w2", WrittenAt: at.Add(time.Minute), BodyPreview: "p2"},
		},
	}
	h := newRecentWritesHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mempalace/recent-writes", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", rec.Code)
	}
	var body recentWritesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v: %s", err, rec.Body.String())
	}
	if len(body.Writes) != 2 {
		t.Errorf("len=%d; want 2", len(body.Writes))
	}
	if body.Writes[0].ID != "d1" {
		t.Errorf("Writes[0].ID=%q; want d1", body.Writes[0].ID)
	}
}

// TestMempalaceHandler_ClampsLimitTo100 — request "?limit=500" → fake
// called with limit=100 (FR-685).
func TestMempalaceHandler_ClampsLimitTo100(t *testing.T) {
	fake := &fakeMempalace{}
	h := newRecentWritesHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mempalace/recent-writes?limit=500", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", rec.Code)
	}
	if !fake.drawersCalled {
		t.Fatal("fake.RecentDrawers not called")
	}
	if fake.gotDrawerLim != 100 {
		t.Errorf("limit=%d; want 100 (clamped)", fake.gotDrawerLim)
	}
}

// TestMempalaceHandler_DefaultsLimitTo30 — request without ?limit →
// fake called with limit=30 (FR-685).
func TestMempalaceHandler_DefaultsLimitTo30(t *testing.T) {
	fake := &fakeMempalace{}
	h := newRecentWritesHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mempalace/recent-writes", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200", rec.Code)
	}
	if fake.gotDrawerLim != 30 {
		t.Errorf("limit=%d; want 30 (default)", fake.gotDrawerLim)
	}
}

// TestMempalaceHandler_SurfacesSidecarUnreachable — fake returns
// ErrSidecarUnreachable → 503 MempalaceUnreachable.
func TestMempalaceHandler_SurfacesSidecarUnreachable(t *testing.T) {
	fake := &fakeMempalace{drawersErr: mempalace.ErrSidecarUnreachable}
	h := newRecentWritesHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mempalace/recent-writes", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d; want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"MempalaceUnreachable"`) {
		t.Errorf("body missing MempalaceUnreachable: %s", rec.Body.String())
	}
}

// TestMempalaceHandler_RecentKGShape — fake KG response → 200 with
// triple shape; triples with optional source_ticket_id pointer
// preserved.
func TestMempalaceHandler_RecentKGShape(t *testing.T) {
	srcTicket := "ticket_42"
	at := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	fake := &fakeMempalace{
		triples: []mempalace.KGTriple{
			{ID: "t1", Subject: "a", Predicate: "b", Object: "c", WrittenAt: at, SourceTicketID: &srcTicket},
			{ID: "t2", Subject: "x", Predicate: "y", Object: "z", WrittenAt: at.Add(time.Minute)}, // no optional fields
		},
	}
	h := newRecentKGHandler(fake, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/mempalace/recent-kg", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d; want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body recentKGResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Facts) != 2 {
		t.Fatalf("len=%d; want 2", len(body.Facts))
	}
	if body.Facts[0].SourceTicketID == nil || *body.Facts[0].SourceTicketID != srcTicket {
		t.Errorf("Facts[0].SourceTicketID=%v; want pointer to %q", body.Facts[0].SourceTicketID, srcTicket)
	}
	if body.Facts[1].SourceTicketID != nil {
		t.Errorf("Facts[1].SourceTicketID=%v; want nil pointer", body.Facts[1].SourceTicketID)
	}
}

// TestMempalaceHandler_RejectsNonGet — POST or PUT against either
// route returns 405. Belt-and-suspenders: SEC-3 carryover insists the
// supervisor proxy is read-only against MemPalace.
func TestMempalaceHandler_RejectsNonGet(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		fake := &fakeMempalace{}
		h := newRecentWritesHandler(fake, nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/mempalace/recent-writes", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("method=%s status=%d; want 405", method, rec.Code)
		}
		if fake.drawersCalled {
			t.Errorf("method=%s: fake should not be called for non-GET", method)
		}
	}
}
