package schedule

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRenderTemplateSubstitutesBothVars(t *testing.T) {
	fireAt := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	lastFiredAt := pgtype.Timestamptz{
		Time:  time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC),
		Valid: true,
	}

	tpl := "Standup digest for {{fire_at}}; cover everything since {{last_fired_at}}."
	got := RenderTemplate(tpl, fireAt, lastFiredAt)
	want := "Standup digest for 2026-06-11T09:00:00Z; cover everything since 2026-06-10T09:00:00Z."
	if got != want {
		t.Fatalf("RenderTemplate = %q, want %q", got, want)
	}

	// Repeated occurrences all substitute.
	tpl = "{{fire_at}} / {{fire_at}} / {{last_fired_at}} / {{last_fired_at}}"
	got = RenderTemplate(tpl, fireAt, lastFiredAt)
	want = "2026-06-11T09:00:00Z / 2026-06-11T09:00:00Z / 2026-06-10T09:00:00Z / 2026-06-10T09:00:00Z"
	if got != want {
		t.Fatalf("RenderTemplate (repeated) = %q, want %q", got, want)
	}

	// Non-UTC inputs render as UTC (FR-103 posture).
	est := time.FixedZone("EST", -5*60*60)
	got = RenderTemplate("{{fire_at}}", time.Date(2026, 6, 11, 4, 0, 0, 0, est), lastFiredAt)
	if got != "2026-06-11T09:00:00Z" {
		t.Fatalf("RenderTemplate (non-UTC fireAt) = %q, want %q", got, "2026-06-11T09:00:00Z")
	}

	// Templates without variables pass through untouched.
	got = RenderTemplate("static text, no variables", fireAt, lastFiredAt)
	if got != "static text, no variables" {
		t.Fatalf("RenderTemplate (static) = %q, want unchanged input", got)
	}
}

func TestRenderTemplateNeverFired(t *testing.T) {
	fireAt := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	never := pgtype.Timestamptz{} // Valid == false: task has never fired

	got := RenderTemplate("Last fired: {{last_fired_at}}.", fireAt, never)
	want := "Last fired: never."
	if got != want {
		t.Fatalf("RenderTemplate (never fired) = %q, want %q", got, want)
	}

	// The literal "never" — not empty, not an error sentinel (FR-107).
	got = RenderTemplate("{{fire_at}} since {{last_fired_at}}", fireAt, never)
	want = "2026-06-11T09:00:00Z since never"
	if got != want {
		t.Fatalf("RenderTemplate (never fired, both vars) = %q, want %q", got, want)
	}
}
