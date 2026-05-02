package dashboardapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeValidator implements SessionValidator with canned outcomes.
type fakeValidator struct {
	userID UserID
	err    error
}

func (f fakeValidator) ValidateCookie(_ context.Context, _ string) (UserID, error) {
	return f.userID, f.err
}

// echoHandler is the wrapped handler under tests; it surfaces the
// UserID injected by the middleware.
func echoHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := UserIDFromContext(r.Context())
		if !ok {
			t.Errorf("expected UserID in context")
			http.Error(w, "no userID", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(string(uid)))
	})
}

// TestAuthMiddleware_RejectsAnonymous — request without the session
// cookie returns 401 AuthExpired.
func TestAuthMiddleware_RejectsAnonymous(t *testing.T) {
	mw := newAuthMiddleware(fakeValidator{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)

	mw(echoHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d; want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"AuthExpired"`) {
		t.Errorf("body missing AuthExpired: %s", rec.Body.String())
	}
}

// TestAuthMiddleware_RejectsInvalidCookie — fake validator returns
// ErrAuthExpired (e.g., token unknown) → 401 AuthExpired.
func TestAuthMiddleware_RejectsInvalidCookie(t *testing.T) {
	mw := newAuthMiddleware(fakeValidator{err: ErrAuthExpired}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "unknown-token"})

	mw(echoHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d; want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"AuthExpired"`) {
		t.Errorf("body missing AuthExpired: %s", rec.Body.String())
	}
}

// TestAuthMiddleware_RejectsExpiredSession — sqlSessionValidator with a
// fake row whose expires_at is in the past returns ErrAuthExpired; the
// middleware then writes 401 AuthExpired. This exercises the
// time-comparison branch end-to-end through the middleware.
func TestAuthMiddleware_RejectsExpiredSession(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	frozen := func() time.Time { return now }

	queryRow := SessionRowQuery(func(_ context.Context, token string) (string, time.Time, error) {
		if token != "valid-but-expired-token" {
			return "", time.Time{}, pgx.ErrNoRows
		}
		// expires_at one minute before "now".
		return "user-uuid-xxx", now.Add(-time.Minute), nil
	})
	v := newSQLSessionValidator(queryRow, frozen)

	mw := newAuthMiddleware(v, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "valid-but-expired-token"})

	mw(echoHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d; want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error":"AuthExpired"`) {
		t.Errorf("body missing AuthExpired: %s", rec.Body.String())
	}
}

// TestAuthMiddleware_AcceptsValidCookie — fake validator returns a valid
// UserID; middleware injects it into request context and invokes the
// wrapped handler.
func TestAuthMiddleware_AcceptsValidCookie(t *testing.T) {
	const wantUID = UserID("user-uuid-aaaa-bbbb-cccc")
	mw := newAuthMiddleware(fakeValidator{userID: wantUID}, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "valid-token"})

	mw(echoHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rec.Code)
	}
	if got := rec.Body.String(); got != string(wantUID) {
		t.Errorf("body=%q; want %q", got, wantUID)
	}
}

// TestSQLSessionValidator_ClassifiesNoRowsAsAuthExpired — direct
// validator-level test for the pgx.ErrNoRows path.
func TestSQLSessionValidator_ClassifiesNoRowsAsAuthExpired(t *testing.T) {
	v := newSQLSessionValidator(func(_ context.Context, _ string) (string, time.Time, error) {
		return "", time.Time{}, pgx.ErrNoRows
	}, nil)
	_, err := v.ValidateCookie(context.Background(), "any-token")
	if !errors.Is(err, ErrAuthExpired) {
		t.Errorf("err=%v; want ErrAuthExpired", err)
	}
}

// TestAuthMiddleware_StripsBetterAuthHMACSignature — better-auth signs
// every issued cookie as `<token>.<hmac>`. The DB stores only the bare
// `<token>` half. The middleware MUST strip everything after the first
// dot before passing to the validator; otherwise every real browser
// session 401s.
func TestAuthMiddleware_StripsBetterAuthHMACSignature(t *testing.T) {
	const bareToken = "8DKqCAb5j99ibY7jgjIg01k31YNawwsX"
	var seenToken string
	validator := fakeValidatorRecorder{
		userID: "user-123",
		recorder: func(token string) {
			seenToken = token
		},
	}
	mw := newAuthMiddleware(validator, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/objstore/company-md", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: bareToken + ".fakehmacsuffix"})

	mw(echoHandler(t)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status=%d; want 200", rec.Code)
	}
	if seenToken != bareToken {
		t.Errorf("validator saw token=%q; want bare %q (signature stripped)", seenToken, bareToken)
	}
}

// fakeValidatorRecorder captures the token argument so we can assert
// that the middleware strips the HMAC signature before passing to
// ValidateCookie.
type fakeValidatorRecorder struct {
	userID   UserID
	recorder func(token string)
}

func (f fakeValidatorRecorder) ValidateCookie(_ context.Context, token string) (UserID, error) {
	if f.recorder != nil {
		f.recorder(token)
	}
	return f.userID, nil
}

// TestSQLSessionValidator_WrapsOtherErrors — non-ErrNoRows errors from
// the row query surface as wrapped non-ErrAuthExpired errors so the
// middleware writes 500 InternalError instead of 401.
func TestSQLSessionValidator_WrapsOtherErrors(t *testing.T) {
	dbBoom := errors.New("connection refused")
	v := newSQLSessionValidator(func(_ context.Context, _ string) (string, time.Time, error) {
		return "", time.Time{}, dbBoom
	}, nil)
	_, err := v.ValidateCookie(context.Background(), "any-token")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrAuthExpired) {
		t.Errorf("non-ErrNoRows must NOT classify as ErrAuthExpired: %v", err)
	}
	if !errors.Is(err, dbBoom) {
		t.Errorf("err=%v; want wrapped %v", err, dbBoom)
	}
}
