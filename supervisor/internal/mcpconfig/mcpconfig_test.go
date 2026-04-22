package mcpconfig

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		t.Fatalf("uuid scan: %v", err)
	}
	return u
}

// TestWriteHappyPath pins the shape of the written file: one mcpServers
// entry named "postgres", command=supervisorBin, args=["mcp-postgres"],
// env.GARRISON_PGMCP_DSN=<dsn>. Permission bit is 0o600.
func TestWriteHappyPath(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "11111111-1111-4111-8111-111111111111")

	path, err := Write(context.Background(), dir, id, "/usr/local/bin/supervisor", "postgres://roonly@localhost/garrison")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if want := filepath.Join(dir, "mcp-config-11111111-1111-4111-8111-111111111111.json"); path != want {
		t.Errorf("path: got %q, want %q", path, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm: got %v, want 0600 (file holds a DSN)", perm)
	}

	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal written file: %v", err)
	}
	pg, ok := cfg.MCPServers["postgres"]
	if !ok {
		t.Fatalf("expected mcpServers.postgres, got keys %v", keys(cfg.MCPServers))
	}
	if pg.Command != "/usr/local/bin/supervisor" {
		t.Errorf("Command: got %q", pg.Command)
	}
	if len(pg.Args) != 1 || pg.Args[0] != "mcp-postgres" {
		t.Errorf("Args: got %v", pg.Args)
	}
	if pg.Env["GARRISON_PGMCP_DSN"] != "postgres://roonly@localhost/garrison" {
		t.Errorf("DSN: got %q", pg.Env["GARRISON_PGMCP_DSN"])
	}
}

// TestWriteIsolationAcrossInvocations verifies two concurrent writes to
// different instance IDs produce two distinct files that do not overlap.
// The supervisor's invariant is one config file per spawn, cleaned up on
// exit; this test pins that the filename-derivation step is keyed to the
// UUID, not to a static path that could be trampled.
func TestWriteIsolationAcrossInvocations(t *testing.T) {
	dir := t.TempDir()
	idA := mustUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	idB := mustUUID(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")

	pathA, err := Write(context.Background(), dir, idA, "/bin/supA", "dsnA")
	if err != nil {
		t.Fatalf("write A: %v", err)
	}
	pathB, err := Write(context.Background(), dir, idB, "/bin/supB", "dsnB")
	if err != nil {
		t.Fatalf("write B: %v", err)
	}
	if pathA == pathB {
		t.Fatalf("expected distinct paths, both are %q", pathA)
	}
	// Both exist, neither clobbered.
	for _, p := range []string{pathA, pathB} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("stat %s: %v", p, err)
		}
	}
	// Content is specific to each invocation.
	aBytes, _ := os.ReadFile(pathA)
	bBytes, _ := os.ReadFile(pathB)
	if !strings.Contains(string(aBytes), "dsnA") || strings.Contains(string(aBytes), "dsnB") {
		t.Errorf("file A content not isolated: %s", aBytes)
	}
	if !strings.Contains(string(bBytes), "dsnB") || strings.Contains(string(bBytes), "dsnA") {
		t.Errorf("file B content not isolated: %s", bBytes)
	}
}

// TestRemoveMissingFile pins FR-103's tolerance: Remove on a missing
// file returns nil, so defer Remove(path) is safe even if an external
// cleanup ran first (spec edge case "MCP config file deleted by an
// external process before subprocess exit").
func TestRemoveMissingFile(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "mcp-config-never-existed.json")
	if err := Remove(nonexistent); err != nil {
		t.Fatalf("Remove on missing file: got %v, want nil", err)
	}
}

// TestRemoveRealFile verifies Remove on an existing file actually
// deletes it, paired with the above to confirm Remove's two modes.
func TestRemoveRealFile(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "22222222-2222-4222-8222-222222222222")
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pre-remove stat: %v", err)
	}
	if err := Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("post-remove stat: got %v, want IsNotExist", err)
	}
}

// fakeOps is the test seam: forces a specific write error (e.g. ENOSPC)
// without needing a RAM-disk or chmod'd directory. Production uses the
// os-backed DefaultOps; injecting fakeOps via WriteWithOps is the
// clarify-Q2 behavioural surface — FR-103 demands we surface the write
// failure verbatim so the caller can set exit_reason='spawn_failed'.
type fakeOps struct {
	writeErr  error
	removeErr error
	writtenAt string
}

func (f *fakeOps) WriteFile(name string, _ []byte, _ fs.FileMode) error {
	f.writtenAt = name
	return f.writeErr
}
func (f *fakeOps) Remove(name string) error { f.writtenAt = name; return f.removeErr }

// TestWriteReturnsErrorOnDiskFull pins clarify-Q2 behaviour: an ENOSPC
// from the underlying file write surfaces as an error so the caller
// (spawn.Spawn) can record exit_reason='spawn_failed' and mark the
// event processed without retry.
func TestWriteReturnsErrorOnDiskFull(t *testing.T) {
	ops := &fakeOps{writeErr: syscall.ENOSPC}
	id := mustUUID(t, "33333333-3333-4333-8333-333333333333")

	_, err := WriteWithOps(context.Background(), ops, "/var/lib/garrison/mcp", id, "/bin/sup", "dsn")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("expected ENOSPC wrapped in the returned error; got %v", err)
	}
	// Ensure the error message mentions the attempted path for operator
	// diagnosability.
	want := "/var/lib/garrison/mcp/mcp-config-33333333-3333-4333-8333-333333333333.json"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error should name path %q, got %q", want, err.Error())
	}
}

// TestWriteReturnsErrorOnPermissionDenied verifies EACCES flows through
// the same error path as ENOSPC. Both satisfy the "record and continue"
// contract in FR-103.
func TestWriteReturnsErrorOnPermissionDenied(t *testing.T) {
	ops := &fakeOps{writeErr: syscall.EACCES}
	id := mustUUID(t, "44444444-4444-4444-8444-444444444444")
	_, err := WriteWithOps(context.Background(), ops, "/var/lib/garrison/mcp", id, "/bin/sup", "dsn")
	if err == nil || !errors.Is(err, syscall.EACCES) {
		t.Fatalf("expected EACCES-wrapped error, got %v", err)
	}
}

func keys(m map[string]struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
},
) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
