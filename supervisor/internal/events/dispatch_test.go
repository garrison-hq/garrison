package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/events"
	"github.com/jackc/pgx/v5/pgtype"
)

const testEventID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

func TestDispatchRoutesKnownChannel(t *testing.T) {
	var gotChannel string
	var gotEventID pgtype.UUID

	d := events.NewDispatcher(map[string]events.Handler{
		"work.ticket.created": func(_ context.Context, id pgtype.UUID) error {
			gotChannel = "work.ticket.created"
			gotEventID = id
			return nil
		},
	})

	payload := `{"event_id":"` + testEventID + `"}`
	if err := d.Dispatch(context.Background(), "work.ticket.created", payload); err != nil {
		t.Fatalf("Dispatch: unexpected error: %v", err)
	}
	if gotChannel != "work.ticket.created" {
		t.Errorf("handler not invoked (gotChannel=%q)", gotChannel)
	}
	if !gotEventID.Valid {
		t.Errorf("handler got invalid event_id")
	}
}

func TestDispatchErrorsOnUnknownChannel(t *testing.T) {
	d := events.NewDispatcher(map[string]events.Handler{
		"work.ticket.created": func(_ context.Context, _ pgtype.UUID) error { return nil },
	})

	payload := `{"event_id":"` + testEventID + `"}`
	err := d.Dispatch(context.Background(), "work.unknown", payload)
	if err == nil {
		t.Fatalf("Dispatch: want error for unknown channel, got nil")
	}
	if !errors.Is(err, events.ErrUnknownChannel) {
		t.Errorf("err = %v, want wrapping ErrUnknownChannel", err)
	}
}

func TestDispatchRejectsMalformedPayload(t *testing.T) {
	d := events.NewDispatcher(map[string]events.Handler{
		"work.ticket.created": func(_ context.Context, _ pgtype.UUID) error {
			t.Fatal("handler must not be called on malformed payload")
			return nil
		},
	})

	err := d.Dispatch(context.Background(), "work.ticket.created", `{not json}`)
	if err == nil {
		t.Fatalf("Dispatch: want error for malformed payload, got nil")
	}
}

// TestDynamicRoutes pins the FR-014 amendment (2026-06-10): the
// dispatcher's roster overlay is swappable at runtime so M7 hires
// dispatch without a restart; the frozen base table wins on conflict.
func TestDynamicRoutes(t *testing.T) {
	var baseHits, dynHits int
	d := events.NewDispatcher(map[string]events.Handler{
		"work.ticket.created.engineering.in_dev": func(_ context.Context, _ pgtype.UUID) error {
			baseHits++
			return nil
		},
	})
	payload := `{"event_id":"` + testEventID + `"}`

	// Unknown before the overlay lands.
	if err := d.Dispatch(context.Background(), "work.ticket.created.marketing.in_dev", payload); !errors.Is(err, events.ErrUnknownChannel) {
		t.Fatalf("pre-overlay dispatch: want ErrUnknownChannel, got %v", err)
	}

	d.SetDynamicRoutes(map[string]events.Handler{
		"work.ticket.created.marketing.in_dev": func(_ context.Context, _ pgtype.UUID) error {
			dynHits++
			return nil
		},
		// Attempted shadow of a base channel — base must win.
		"work.ticket.created.engineering.in_dev": func(_ context.Context, _ pgtype.UUID) error {
			t.Fatal("dynamic route must not shadow the frozen base table")
			return nil
		},
	})

	if err := d.Dispatch(context.Background(), "work.ticket.created.marketing.in_dev", payload); err != nil {
		t.Fatalf("overlay dispatch: %v", err)
	}
	if err := d.Dispatch(context.Background(), "work.ticket.created.engineering.in_dev", payload); err != nil {
		t.Fatalf("base dispatch: %v", err)
	}
	if baseHits != 1 || dynHits != 1 {
		t.Errorf("hits base=%d dyn=%d; want 1/1", baseHits, dynHits)
	}

	// Channels() reports the union without duplicating shadowed names.
	chs := d.Channels()
	seen := map[string]int{}
	for _, c := range chs {
		seen[c]++
	}
	if seen["work.ticket.created.marketing.in_dev"] != 1 || seen["work.ticket.created.engineering.in_dev"] != 1 {
		t.Errorf("Channels() = %v; want union with no duplicates", chs)
	}

	// Replacing the overlay drops routes that vanished from the roster.
	d.SetDynamicRoutes(nil)
	if err := d.Dispatch(context.Background(), "work.ticket.created.marketing.in_dev", payload); !errors.Is(err, events.ErrUnknownChannel) {
		t.Fatalf("post-clear dispatch: want ErrUnknownChannel, got %v", err)
	}
}
