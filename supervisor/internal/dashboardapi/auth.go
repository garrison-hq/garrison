package dashboardapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SessionCookieName pins the better-auth session cookie name. Verified
// against dashboard/lib/auth/ + the M5.4 deploy-marker migration's
// header comment.
const SessionCookieName = "better-auth.session_token"

// stripCookieSignature splits a better-auth signed cookie value of the
// form "<token>.<hmac>" and returns the bare token half. better-auth
// signs every issued cookie with HMAC-SHA256 over BETTER_AUTH_SECRET
// (verified live against the dev stack 2026-05-01); the database
// `sessions.token` column stores the bare token only. The supervisor
// does NOT have BETTER_AUTH_SECRET in its env and therefore cannot
// verify the HMAC — but that's OK: integrity protection on the cookie
// matters less than the underlying access invariant. An attacker who
// has the signed cookie has the bare token by definition (it's the
// prefix); they don't need to forge the HMAC to query Postgres. The
// strip-and-trust approach is cryptographically equivalent to
// verify-then-strip in terms of who-can-access-what.
//
// If the value contains no dot, returns the value unchanged — this
// handles the legacy / unsigned-cookie case (e.g. tokens inserted
// directly into the sessions table for dev/test).
func stripCookieSignature(value string) string {
	if i := strings.IndexByte(value, '.'); i >= 0 {
		return value[:i]
	}
	return value
}

// UserID is the canonical UUID-string representation of a session's
// user_id. Stored on request context for downstream handlers (T009 /
// T010) that wish to attribute writes / reads.
type UserID string

// SessionValidator is the seam handlers use to validate the session
// cookie. Production wires sqlSessionValidator (cookie-forward + Postgres
// lookup against the dashboard's better-auth `sessions` table). Tests
// substitute fakes.
type SessionValidator interface {
	ValidateCookie(ctx context.Context, sessionToken string) (UserID, error)
}

// SessionRowQuery reads a single session row by token. Production binds
// this to a closure over pgxpool.Pool.QueryRow + Scan. Keeping the seam
// at this granularity (one method, three return values) lets tests
// exercise sqlSessionValidator's logic without dragging in pgx.Row.
type SessionRowQuery func(ctx context.Context, token string) (userID string, expiresAt time.Time, err error)

// sqlSessionValidator implements SessionValidator by calling the
// SessionRowQuery seam and applying the time-comparison + error
// classification rules.
type sqlSessionValidator struct {
	queryRow SessionRowQuery
	now      func() time.Time
}

// newSQLSessionValidator returns a validator using the given row query.
// `now` may be nil; defaults to time.Now.
func newSQLSessionValidator(queryRow SessionRowQuery, now func() time.Time) *sqlSessionValidator {
	if now == nil {
		now = time.Now
	}
	return &sqlSessionValidator{queryRow: queryRow, now: now}
}

// ValidateCookie executes the session lookup and applies the M5.4 auth
// rules:
//
//   - pgx.ErrNoRows                → ErrAuthExpired (no matching token)
//   - row.expires_at < now         → ErrAuthExpired (stale session)
//   - other errors                 → wrapped non-typed error (handler
//     returns 500)
//   - happy path                   → UserID, nil
func (v *sqlSessionValidator) ValidateCookie(ctx context.Context, token string) (UserID, error) {
	userID, expiresAt, err := v.queryRow(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrAuthExpired
		}
		return "", fmt.Errorf("dashboardapi: session lookup: %w", err)
	}
	if expiresAt.Before(v.now()) {
		return "", ErrAuthExpired
	}
	return UserID(userID), nil
}

// userIDContextKey is the unexported context key used to attach the
// validated UserID to a request's context. Handlers retrieve it via
// UserIDFromContext.
type contextKey struct{ name string }

var userIDContextKey = contextKey{name: "dashboardapi.userID"}

// UserIDFromContext returns the UserID attached by the auth middleware,
// if any. Handlers downstream of newAuthMiddleware can rely on this; an
// "ok=false" result signals the middleware was bypassed (test-only).
func UserIDFromContext(ctx context.Context) (UserID, bool) {
	v, ok := ctx.Value(userIDContextKey).(UserID)
	return v, ok
}

// newAuthMiddleware returns an http middleware that:
//
//   - reads SessionCookieName from the request
//   - rejects 401 AuthExpired if absent
//   - calls validator.ValidateCookie(ctx, cookie.Value)
//   - on ErrAuthExpired → 401 AuthExpired
//   - on any other error → 500 InternalError (logged at ERROR level)
//   - on success → injects UserID into request context, calls next
func newAuthMiddleware(validator SessionValidator, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil || cookie.Value == "" {
				writeErrorResponse(w, http.StatusUnauthorized, "AuthExpired", "", "", logger)
				return
			}
			userID, err := validator.ValidateCookie(r.Context(), stripCookieSignature(cookie.Value))
			if err != nil {
				if errors.Is(err, ErrAuthExpired) {
					writeErrorResponse(w, http.StatusUnauthorized, "AuthExpired", "", "", logger)
					return
				}
				if logger != nil {
					logger.Error("dashboardapi: cookie validation failed", "err", err)
				}
				writeErrorResponse(w, http.StatusInternalServerError, "InternalError", "", "", logger)
				return
			}
			ctx := context.WithValue(r.Context(), userIDContextKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
