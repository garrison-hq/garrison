package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// TestApplyAgentROPasswordValue_EmptyIsWarning pins the early-exit path:
// when the password is empty the function logs a warning, does not
// touch the database (db may be nil), and returns nil so `--migrate`
// can still complete.
func TestApplyAgentROPasswordValue_EmptyIsWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	if err := applyAgentROPasswordValue(context.Background(), nil, logger, ""); err != nil {
		t.Fatalf("empty password should return nil, got %v", err)
	}

	// The warning message is load-bearing for operator observability —
	// pin it so a future refactor doesn't silently drop it.
	if !strings.Contains(buf.String(), "GARRISON_AGENT_RO_PASSWORD is unset") {
		t.Fatalf("missing warning log: %s", buf.String())
	}
}

// TestApplyAgentROPasswordValue_NULByteRejected pins the NUL-byte
// guard. Postgres rejects NULs in role passwords server-side, but we
// reject up front for a clearer error message. This path is only
// reachable by direct-value injection — `os.Setenv` rejects NUL
// bytes at the syscall layer.
func TestApplyAgentROPasswordValue_NULByteRejected(t *testing.T) {
	err := applyAgentROPasswordValue(
		context.Background(), nil, slog.New(slog.DiscardHandler),
		"good-prefix\x00bad-suffix",
	)
	if err == nil {
		t.Fatal("password with NUL byte should error")
	}
	if !strings.Contains(err.Error(), "NUL byte") {
		t.Fatalf("error should mention NUL byte, got: %v", err)
	}
}

// TestApplyAgentROPassword_ReadsEnv pins the thin wrapper: unset env
// → delegates to the empty-string branch of applyAgentROPasswordValue
// → returns nil without touching db.
func TestApplyAgentROPassword_ReadsEnv(t *testing.T) {
	t.Setenv("GARRISON_AGENT_RO_PASSWORD", "")

	if err := applyAgentROPassword(context.Background(), nil, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("unset env should return nil, got %v", err)
	}
}

// TestOpenMigrationDB_BadURL pins the malformed-URL error path.
// pgxpool.ParseConfig rejects unparseable DSNs; the function wraps
// that error with a `parse url:` prefix.
func TestOpenMigrationDB_BadURL(t *testing.T) {
	// postgres:// with an unclosed IPv6 literal is syntactically broken.
	_, err := openMigrationDB("postgres://[::1")
	if err == nil {
		t.Fatal("malformed URL should error")
	}
	if !strings.Contains(err.Error(), "parse url") {
		t.Fatalf("error should be prefixed with 'parse url', got: %v", err)
	}
}

// TestOpenMigrationDB_ValidURL pins that a well-formed DSN returns a
// non-nil *sql.DB without attempting to actually connect (pgx defers
// dial until first use).
func TestOpenMigrationDB_ValidURL(t *testing.T) {
	db, err := openMigrationDB("postgres://user:pass@localhost:5432/garrison?sslmode=disable")
	if err != nil {
		t.Fatalf("valid URL should parse, got %v", err)
	}
	if db == nil {
		t.Fatal("valid URL should return non-nil *sql.DB")
	}
	_ = db.Close()
}

// TestGooseSlogAdapter_Printf pins that the adapter forwards its
// formatted message into the underlying slog.Logger. Goose calls
// Printf on every migration-progress line; the adapter keeps that
// output in the same JSON stream as the rest of the supervisor.
func TestGooseSlogAdapter_Printf(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	adapter := &gooseSlogAdapter{logger: logger}

	adapter.Printf("applied migration %s in %dms", "20260422000003", 42)

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("expected valid JSON log, got %q: %v", buf.String(), err)
	}
	if lvl, _ := record["level"].(string); lvl != "INFO" {
		t.Errorf("expected level=INFO, got %v", record["level"])
	}
	if msg, _ := record["msg"].(string); !strings.Contains(msg, "20260422000003") || !strings.Contains(msg, "42ms") {
		t.Errorf("message lost Printf args: %q", msg)
	}
}
