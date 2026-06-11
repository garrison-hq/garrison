package mcpjungle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

// stubServer wraps httptest.Server with a handler that asserts on the
// admin bearer header so every test can verify auth injection without
// repeating boilerplate.
type stubServer struct {
	*httptest.Server
	gotAuth string
}

func newStubServer(t *testing.T, handler http.HandlerFunc) *stubServer {
	t.Helper()
	s := &stubServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.gotAuth = r.Header.Get("Authorization")
		handler(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCreateMcpClientHappyPath(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v0/clients" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "garrison.engineer.deadbeef" {
			t.Errorf("name = %v; want garrison.engineer.deadbeef", body["name"])
		}
		w.WriteHeader(http.StatusCreated)
		// Mirrors the live API's GORM-shaped body: numeric "ID".
		_ = json.NewEncoder(w).Encode(map[string]any{"ID": 4, "name": "garrison.engineer.deadbeef"})
	})
	c := NewClient(s.URL, "admin-tok", nil)
	res, err := c.CreateMcpClient(context.Background(), CreateMcpClientParams{
		Name:        "garrison.engineer.deadbeef",
		AllowList:   []string{"garrison.linear"},
		AccessToken: "agent-bearer",
	})
	if err != nil {
		t.Fatalf("CreateMcpClient: %v", err)
	}
	if res.ID != "4" {
		t.Errorf("ID = %q; want 4", res.ID)
	}
	if s.gotAuth != "Bearer admin-tok" {
		t.Errorf("Authorization header = %q; want Bearer admin-tok", s.gotAuth)
	}
}

func TestCreateMcpClientConflictIsTypedError(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	_, err := c.CreateMcpClient(context.Background(), CreateMcpClientParams{
		Name: "dup", AccessToken: "tok",
	})
	if !errors.Is(err, ErrServerRegistrationConflict) {
		t.Errorf("err = %v; want ErrServerRegistrationConflict", err)
	}
}

func TestCreateMcpClientUnauthorized(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := NewClient(s.URL, "bad-tok", nil)
	_, err := c.CreateMcpClient(context.Background(), CreateMcpClientParams{
		Name: "x", AccessToken: "tok",
	})
	if !errors.Is(err, ErrAdminTokenInvalid) {
		t.Errorf("err = %v; want ErrAdminTokenInvalid", err)
	}
}

func TestRegisterServerHappyPath(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/servers" {
			t.Errorf("path = %s; want /api/v0/servers", r.URL.Path)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "server-42"})
	})
	c := NewClient(s.URL, "admin-tok", nil)
	id, err := c.RegisterServer(context.Background(), ServerSpec{
		Name:      "garrison.linear",
		Transport: "http",
		URL:       "http://linear-mcp:9000",
	})
	if err != nil {
		t.Fatalf("RegisterServer: %v", err)
	}
	if id != "server-42" {
		t.Errorf("id = %q; want server-42", id)
	}
}

func TestRegisterServerConflict(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	_, err := c.RegisterServer(context.Background(), ServerSpec{Name: "dup"})
	if !errors.Is(err, ErrServerRegistrationConflict) {
		t.Errorf("err = %v; want ErrServerRegistrationConflict", err)
	}
}

func TestHealthCheckSurfacesUnreachable(t *testing.T) {
	// Point at a port nothing is listening on so Do() returns a conn
	// refused. We use a closed httptest server.
	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	s.Close()
	c := NewClient(s.URL, "admin-tok", nil)
	err := c.HealthCheck(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Errorf("err = %v; want ErrUnreachable", err)
	}
}

func TestHealthCheckAdminTokenRejected(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %s; want /health", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := NewClient(s.URL, "bad-tok", nil)
	err := c.HealthCheck(context.Background())
	if !errors.Is(err, ErrAdminTokenInvalid) {
		t.Errorf("err = %v; want ErrAdminTokenInvalid", err)
	}
}

func TestHealthCheckHappyPath(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	if err := c.HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestURLForCustomerAlphaReturnsBaseURL(t *testing.T) {
	c := NewClient("http://garrison-mcpjungle:8080/", "admin-tok", nil)
	// Trailing slash is stripped at construction time.
	if c.BaseURL != "http://garrison-mcpjungle:8080" {
		t.Errorf("BaseURL = %q; want trimmed", c.BaseURL)
	}
	var someCustomer pgtype.UUID
	_ = someCustomer.Scan("11111111-2222-3333-4444-555555555555")
	if got := c.URLForCustomer(someCustomer); got != c.BaseURL {
		t.Errorf("URLForCustomer = %q; want %q (M8 alpha: ignores customer)", got, c.BaseURL)
	}
	var zero pgtype.UUID
	if got := c.URLForCustomer(zero); got != c.BaseURL {
		t.Errorf("URLForCustomer(zero) = %q; want %q", got, c.BaseURL)
	}
}

func TestUpdateAllowListNotFound(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v0/clients/") {
			t.Errorf("path = %s; want /api/v0/clients/...", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	err := c.UpdateAllowList(context.Background(), "missing", []string{"x"})
	if !errors.Is(err, ErrClientNotFound) {
		t.Errorf("err = %v; want ErrClientNotFound", err)
	}
}

func TestDeleteMcpClientNotFound(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	err := c.DeleteMcpClient(context.Background(), "missing")
	if !errors.Is(err, ErrClientNotFound) {
		t.Errorf("err = %v; want ErrClientNotFound", err)
	}
}

func TestDeregisterServerNotFound(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	err := c.DeregisterServer(context.Background(), "missing")
	if !errors.Is(err, ErrServerNotFound) {
		t.Errorf("err = %v; want ErrServerNotFound", err)
	}
}
