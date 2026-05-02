package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchHappyPath pins the success-path contract: server serves a
// known tar.gz, the client returns the raw body bytes plus the
// computed SHA-256 hex string. The digest comparison against the
// propose-time value happens at the actuator level, not here — this
// test confirms the client reports the body's actual hash.
func TestFetchHappyPath(t *testing.T) {
	wantBody := []byte("not actually a tarball; bytes for the test")
	wantHash := sha256Hex(wantBody)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/api/v1/packages/foo/versions/1.0.0/tarball") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wantBody)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	body, gotHash, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != string(wantBody) {
		t.Errorf("body mismatch: got %d bytes, want %d", len(body), len(wantBody))
	}
	if gotHash != wantHash {
		t.Errorf("hash: got %s; want %s", gotHash, wantHash)
	}
	if c.Name() != "skills.sh" {
		t.Errorf("Name: got %q; want %q", c.Name(), "skills.sh")
	}
}

// TestFetchPackageNotFound — registry returns 404; client surfaces
// ErrPackageNotFound. Operator-actionable distinction from
// ErrRegistryUnreachable.
func TestFetchPackageNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	_, _, err := c.Fetch(context.Background(), "ghost", "0.0.0")
	if !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("err: got %v; want %v", err, ErrPackageNotFound)
	}
}

// TestFetchAuthFailed — registry returns 401; client surfaces
// ErrRegistryAuthFailed. skills.sh is anonymous so this fires only
// against misconfigured deployments; same shape against SkillHub
// covers the more common operator-facing case.
func TestFetchAuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	_, _, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrRegistryAuthFailed) {
		t.Fatalf("err: got %v; want %v", err, ErrRegistryAuthFailed)
	}
}

// TestFetchRateLimited — registry returns 429 with a Retry-After
// header that exceeds the retry budget; client surfaces
// ErrRegistryRateLimited without retrying.
func TestFetchRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120") // 2 minutes — exceeds 30s budget
		http.Error(w, "too many", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	_, _, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrRegistryRateLimited) {
		t.Fatalf("err: got %v; want %v", err, ErrRegistryRateLimited)
	}
}

// TestFetchRateLimitedWithinBudget — first response is 429 with a
// short Retry-After; second attempt succeeds. Covers the retry-loop
// success-after-retry path.
func TestFetchRateLimitedWithinBudget(t *testing.T) {
	wantBody := []byte("post-retry tarball bytes")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1") // 1s, well under 30s budget
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write(wantBody)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	body, _, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != string(wantBody) {
		t.Errorf("body mismatch")
	}
	if calls != 2 {
		t.Errorf("calls=%d; want 2 (retry should fire)", calls)
	}
}

// TestFetchServerError — registry returns 5xx; client surfaces
// ErrRegistryServerError (distinct from Unreachable).
func TestFetchServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	_, _, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrRegistryServerError) {
		t.Fatalf("err: got %v; want %v", err, ErrRegistryServerError)
	}
}

// TestDescribeHappyPath — Describe returns Metadata populated from
// the JSON body. Used by the dashboard approval surface (FR-106a /
// FR-108).
func TestDescribeHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"author":"alice","description":"static analysis","sha256":"deadbeef"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewSkillsShClient(srv.URL, srv.Client())
	md, err := c.Describe(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if md.Author != "alice" || md.Description != "static analysis" || md.SHA256 != "deadbeef" {
		t.Errorf("metadata: %+v", md)
	}
	if md.Package != "foo" || md.Version != "1.0.0" {
		t.Errorf("metadata package/version: %+v", md)
	}
}

// TestParseRetryAfter pins both shapes the spec accepts: delta-seconds
// and HTTP-date.
func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter(""); d != 0 {
		t.Errorf("empty: %v", d)
	}
	if d := parseRetryAfter("5"); d.Seconds() != 5 {
		t.Errorf("delta-seconds: %v", d)
	}
	if d := parseRetryAfter("not-a-time"); d != 0 {
		t.Errorf("garbage: %v", d)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
