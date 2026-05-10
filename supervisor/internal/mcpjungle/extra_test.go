package mcpjungle

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// Extra unit tests covering the paths the integration suite doesn't
// reach (UpdateAllowList happy path, DeleteMcpClient/DeregisterServer
// happy paths, do() unreachable handling).

func TestUpdateAllowListHappyPath(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s; want PATCH", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	if err := c.UpdateAllowList(context.Background(), "garrison.engineer.abc", []string{"garrison.linear"}); err != nil {
		t.Errorf("UpdateAllowList: %v", err)
	}
}

func TestUpdateAllowList204(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	if err := c.UpdateAllowList(context.Background(), "x", nil); err != nil {
		t.Errorf("204 should succeed: %v", err)
	}
}

func TestUpdateAllowListUnauthorized(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := NewClient(s.URL, "bad-tok", nil)
	err := c.UpdateAllowList(context.Background(), "x", nil)
	if !errors.Is(err, ErrAdminTokenInvalid) {
		t.Errorf("err = %v; want ErrAdminTokenInvalid", err)
	}
}

func TestUpdateAllowListGenericErrorSurfaces(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("oops"))
	})
	c := NewClient(s.URL, "admin-tok", nil)
	err := c.UpdateAllowList(context.Background(), "x", nil)
	if err == nil {
		t.Errorf("err should surface 500; got nil")
	}
}

func TestDeleteMcpClientHappyPath(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s; want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	if err := c.DeleteMcpClient(context.Background(), "garrison.x"); err != nil {
		t.Errorf("DeleteMcpClient: %v", err)
	}
}

func TestDeleteMcpClientUnauthorized(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := NewClient(s.URL, "bad-tok", nil)
	err := c.DeleteMcpClient(context.Background(), "x")
	if !errors.Is(err, ErrAdminTokenInvalid) {
		t.Errorf("err = %v; want ErrAdminTokenInvalid", err)
	}
}

func TestDeregisterServerHappyPath(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c := NewClient(s.URL, "admin-tok", nil)
	if err := c.DeregisterServer(context.Background(), "garrison.linear"); err != nil {
		t.Errorf("DeregisterServer: %v", err)
	}
}

func TestDeregisterServerUnauthorized(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := NewClient(s.URL, "bad-tok", nil)
	err := c.DeregisterServer(context.Background(), "x")
	if !errors.Is(err, ErrAdminTokenInvalid) {
		t.Errorf("err = %v; want ErrAdminTokenInvalid", err)
	}
}

func TestRegisterServerUnauthorized(t *testing.T) {
	s := newStubServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := NewClient(s.URL, "bad-tok", nil)
	_, err := c.RegisterServer(context.Background(), ServerSpec{Name: "x", Transport: "http", URL: "https://y"})
	if !errors.Is(err, ErrAdminTokenInvalid) {
		t.Errorf("err = %v; want ErrAdminTokenInvalid", err)
	}
}

func TestNewClientNilLoggerFallsBack(t *testing.T) {
	c := NewClient("http://x", "tok", nil)
	if c.Logger == nil {
		t.Error("Logger should fall back to slog.Default when nil")
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://x/", "tok", nil)
	if c.BaseURL != "http://x" {
		t.Errorf("BaseURL = %q; want http://x", c.BaseURL)
	}
}
