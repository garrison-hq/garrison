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
