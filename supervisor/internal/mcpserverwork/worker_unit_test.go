package mcpserverwork

import (
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/mcpjungle"
	"github.com/garrison-hq/garrison/supervisor/internal/store"
)

func TestNewRejectsNilQueries(t *testing.T) {
	_, err := New(Deps{Client: &mcpjungle.Client{}})
	if err == nil {
		t.Error("expected error on nil Queries")
	}
}

func TestNewRejectsNilClient(t *testing.T) {
	_, err := New(Deps{Queries: &store.Queries{}})
	if err == nil {
		t.Error("expected error on nil Client")
	}
}

func TestNewFallsBackToDefaultLogger(t *testing.T) {
	w, err := New(Deps{
		Queries: &store.Queries{},
		Client:  &mcpjungle.Client{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w == nil {
		t.Fatal("worker is nil")
	}
	// Logger should be populated (Default).
	if w.deps.Logger == nil {
		t.Error("Logger should fall back to slog.Default")
	}
}

func TestChannelConstant(t *testing.T) {
	if Channel != "work.mcp_server.registration_requested" {
		t.Errorf("Channel = %q; want work.mcp_server.registration_requested", Channel)
	}
}
