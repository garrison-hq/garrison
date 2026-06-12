package schedule

import (
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// RenderTemplate substitutes the exactly-two template variables
// (FR-107) into an objective / acceptance-criteria template:
//
//	{{fire_at}}        — the firing timestamp, RFC 3339 UTC
//	{{last_fired_at}}  — the previous firing's timestamp, RFC 3339
//	                     UTC, or the literal "never" when the task
//	                     has never fired
//
// Plain strings.ReplaceAll — no text/template engine, so there is no
// injection surface and no error path. No other templating
// capability ships.
func RenderTemplate(tpl string, fireAt time.Time, lastFiredAt pgtype.Timestamptz) string {
	last := "never"
	if lastFiredAt.Valid {
		last = lastFiredAt.Time.UTC().Format(time.RFC3339)
	}
	out := strings.ReplaceAll(tpl, "{{fire_at}}", fireAt.UTC().Format(time.RFC3339))
	return strings.ReplaceAll(out, "{{last_fired_at}}", last)
}
