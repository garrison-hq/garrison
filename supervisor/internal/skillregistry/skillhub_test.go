package skillregistry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSkillHubFetchHappyPath — bearer-token auth + tarball delivery.
// Confirms the Authorization header is set on the outbound request.
func TestSkillHubFetchHappyPath(t *testing.T) {
	wantBody := []byte("self-hosted skillhub tarball bytes")

	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		if !strings.Contains(r.URL.Path, "/api/skills/foo/versions/1.0.0/tarball") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(wantBody)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillHubClient(srv.URL, "test-admin-token", srv.Client())
	body, hash, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(body) != string(wantBody) {
		t.Errorf("body mismatch")
	}
	if hash != sha256Hex(wantBody) {
		t.Errorf("hash mismatch")
	}
	if sawAuth != "Bearer test-admin-token" {
		t.Errorf("Authorization header: got %q; want %q", sawAuth, "Bearer test-admin-token")
	}
	if c.Name() != "skillhub" {
		t.Errorf("Name: got %q; want %q", c.Name(), "skillhub")
	}
}

// TestSkillHubMissingTokenIsAuthFailed — empty token short-circuits
// the HTTP call and returns ErrRegistryAuthFailed without a network
// roundtrip. Operator-actionable: configure GARRISON_SKILLHUB_TOKEN.
func TestSkillHubMissingTokenIsAuthFailed(t *testing.T) {
	c := NewSkillHubClient("http://unreachable.invalid", "", nil)

	_, _, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrRegistryAuthFailed) {
		t.Fatalf("Fetch err: got %v; want %v", err, ErrRegistryAuthFailed)
	}
	if !strings.Contains(err.Error(), "GARRISON_SKILLHUB_TOKEN") {
		t.Errorf("error should name the env var: %v", err)
	}

	_, err = c.Describe(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrRegistryAuthFailed) {
		t.Fatalf("Describe err: got %v; want %v", err, ErrRegistryAuthFailed)
	}
}

// TestSkillHubExpiredTokenSurfacesAuthFailed — server returns 401;
// client surfaces ErrRegistryAuthFailed mentioning token rotation.
func TestSkillHubExpiredTokenSurfacesAuthFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillHubClient(srv.URL, "stale-token", srv.Client())
	_, _, err := c.Fetch(context.Background(), "foo", "1.0.0")
	if !errors.Is(err, ErrRegistryAuthFailed) {
		t.Fatalf("err: got %v; want %v", err, ErrRegistryAuthFailed)
	}
	if !strings.Contains(err.Error(), "rotate GARRISON_SKILLHUB_TOKEN") {
		t.Errorf("error should suggest token rotation: %v", err)
	}
}

// TestSkillHubPackageNotFound — server returns 404; client surfaces
// ErrPackageNotFound (operator-actionable: check proposal pkg/version).
func TestSkillHubPackageNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := NewSkillHubClient(srv.URL, "token", srv.Client())
	_, _, err := c.Fetch(context.Background(), "ghost", "0.0.0")
	if !errors.Is(err, ErrPackageNotFound) {
		t.Fatalf("err: got %v; want %v", err, ErrPackageNotFound)
	}
}

// TestSkillHubDescribeHappyPath — JSON metadata round-trip.
func TestSkillHubDescribeHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"author":"bob","description":"sast bridge","sha256":"cafef00d"}`))
	}))
	t.Cleanup(srv.Close)

	c := NewSkillHubClient(srv.URL, "token", srv.Client())
	md, err := c.Describe(context.Background(), "sast", "v2")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if md.Author != "bob" || md.Description != "sast bridge" || md.SHA256 != "cafef00d" {
		t.Errorf("metadata: %+v", md)
	}
}
