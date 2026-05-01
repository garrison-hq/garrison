package garrisonmutate

import (
	"github.com/jackc/pgx/v5/pgtype"
)

// uuidString stringifies a pgtype.UUID into canonical 8-4-4-4-12 form.
// Returns "" for invalid (Valid==false) values; callers log this case
// rather than treating it as an error.
func uuidString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	v, err := u.Value()
	if err != nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
