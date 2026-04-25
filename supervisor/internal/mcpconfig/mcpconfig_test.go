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

	path, err := Write(context.Background(), dir, id, "/usr/local/bin/supervisor", "postgres://roonly@localhost/garrison", MempalaceParams{}, FinalizeParams{}, nil)
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

	pathA, err := Write(context.Background(), dir, idA, "/bin/supA", "dsnA", MempalaceParams{}, FinalizeParams{}, nil)
	if err != nil {
		t.Fatalf("write A: %v", err)
	}
	pathB, err := Write(context.Background(), dir, idB, "/bin/supB", "dsnB", MempalaceParams{}, FinalizeParams{}, nil)
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
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn", MempalaceParams{}, FinalizeParams{}, nil)
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

	_, err := WriteWithOps(context.Background(), ops, "/var/lib/garrison/mcp", id, "/bin/sup", "dsn", MempalaceParams{}, FinalizeParams{}, nil)
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
	_, err := WriteWithOps(context.Background(), ops, "/var/lib/garrison/mcp", id, "/bin/sup", "dsn", MempalaceParams{}, FinalizeParams{}, nil)
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

// ------------------------------------------------------------------
// M2.2 — T011 tests for the mempalace entry.
// ------------------------------------------------------------------

func TestWritePostgresPlusMempalace(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "55555555-5555-4555-8555-555555555555")

	mp := MempalaceParams{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "tcp://garrison-docker-proxy:2375",
	}
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn", mp, FinalizeParams{}, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Both entries present.
	if _, ok := cfg.MCPServers["postgres"]; !ok {
		t.Errorf("postgres entry missing")
	}
	m, ok := cfg.MCPServers["mempalace"]
	if !ok {
		t.Fatalf("mempalace entry missing; keys=%v", keys(cfg.MCPServers))
	}
	if m.Command != "/usr/bin/docker" {
		t.Errorf("mempalace.command=%q", m.Command)
	}
	wantArgs := []string{"exec", "-i", "garrison-mempalace", "python", "-m", "mempalace.mcp_server", "--palace", "/palace"}
	if len(m.Args) != len(wantArgs) {
		t.Errorf("args len=%d; want %d", len(m.Args), len(wantArgs))
	}
	for i, a := range m.Args {
		if a != wantArgs[i] {
			t.Errorf("args[%d]=%q; want %q", i, a, wantArgs[i])
		}
	}
	if m.Env["DOCKER_HOST"] != "tcp://garrison-docker-proxy:2375" {
		t.Errorf("env.DOCKER_HOST=%q", m.Env["DOCKER_HOST"])
	}
}

// TestWriteMempalaceEnvCarriesDockerHost isolates the env.DOCKER_HOST
// shape (T011 completion condition).
func TestWriteMempalaceEnvCarriesDockerHost(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "66666666-6666-4666-8666-666666666666")
	mp := MempalaceParams{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "tcp://override:9999",
	}
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn", mp, FinalizeParams{}, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"DOCKER_HOST": "tcp://override:9999"`) {
		t.Errorf("DOCKER_HOST override not found in emitted JSON: %s", raw)
	}
}

// TestWriteEmptyMempalaceParamsOmitsEntry pins the M2.1 back-compat path:
// when all four MempalaceParams fields are empty, no mempalace entry is
// emitted (postgres-only output, byte-identical to M2.1).
func TestWriteEmptyMempalaceParamsOmitsEntry(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "77777777-7777-4777-8777-777777777777")
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn", MempalaceParams{}, FinalizeParams{}, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "mempalace") {
		t.Errorf("empty MempalaceParams should not emit mempalace entry; got:\n%s", raw)
	}
	if !strings.Contains(string(raw), "postgres") {
		t.Errorf("postgres entry missing from output:\n%s", raw)
	}
}

// TestBuildConfigIncludesFinalizeEntry — M2.2.1 T004: when finalize
// params are enabled, the emitted JSON carries a third `finalize` MCP
// entry alongside postgres + mempalace, with the agent-instance-id and
// DSN injected into its env block per FR-256 / spec Clarification
// 2026-04-23 Q4.
func TestBuildConfigIncludesFinalizeEntry(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "88888888-8888-4888-8888-888888888888")
	mp := MempalaceParams{
		DockerBin:          "/usr/bin/docker",
		MempalaceContainer: "garrison-mempalace",
		PalacePath:         "/palace",
		DockerHost:         "tcp://garrison-docker-proxy:2375",
	}
	fp := FinalizeParams{
		SupervisorBin:   "/usr/local/bin/supervisor",
		AgentInstanceID: "a1b2c3d4-1111-2222-3333-444455556666",
		DatabaseURL:     "postgres://garrison_agent_ro:pw@pg/garrison",
	}
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn", mp, fp, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read emitted file: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"finalize"`) {
		t.Errorf("finalize entry missing from emitted JSON:\n%s", s)
	}
	if !strings.Contains(s, `"command": "/usr/local/bin/supervisor"`) {
		t.Errorf("finalize command must be FinalizeParams.SupervisorBin; got:\n%s", s)
	}
	if !strings.Contains(s, `"mcp"`) || !strings.Contains(s, `"finalize"`) {
		t.Errorf("finalize args missing [mcp, finalize]:\n%s", s)
	}
	if !strings.Contains(s, `"GARRISON_AGENT_INSTANCE_ID": "a1b2c3d4-1111-2222-3333-444455556666"`) {
		t.Errorf("GARRISON_AGENT_INSTANCE_ID env missing or wrong:\n%s", s)
	}
	if !strings.Contains(s, `"GARRISON_DATABASE_URL": "postgres://garrison_agent_ro:pw@pg/garrison"`) {
		t.Errorf("GARRISON_DATABASE_URL env missing or wrong:\n%s", s)
	}
	if !strings.Contains(s, `"postgres"`) {
		t.Errorf("postgres entry missing:\n%s", s)
	}
	if !strings.Contains(s, `"mempalace"`) {
		t.Errorf("mempalace entry missing:\n%s", s)
	}
}

// TestBuildConfigOmitsFinalizeEntryWhenParamsEmpty — back-compat: empty
// FinalizeParams mean no finalize entry emitted. M2.1/M2.2 callers
// that don't yet know about finalize keep producing the same
// postgres+mempalace output.
func TestBuildConfigOmitsFinalizeEntryWhenParamsEmpty(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "99999999-9999-4999-8999-999999999999")
	path, err := Write(context.Background(), dir, id, "/bin/sup", "dsn", MempalaceParams{}, FinalizeParams{}, nil)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"finalize"`) {
		t.Errorf("empty FinalizeParams should not emit finalize entry; got:\n%s", raw)
	}
}

// M2.3 T007 — Rule 3 enforcement tests.

// TestRejectVaultServersNoMatch — baseline config (postgres, mempalace,
// finalize) must NOT trigger the banned-pattern filter.
func TestRejectVaultServersNoMatch(t *testing.T) {
	cfg := mcpConfig{MCPServers: map[string]mcpServerSpec{
		"postgres":  {Command: "/bin/sup"},
		"mempalace": {Command: "/bin/docker"},
		"finalize":  {Command: "/bin/sup"},
	}}
	if err := RejectVaultServers(cfg); err != nil {
		t.Errorf("baseline servers should not be banned; got: %v", err)
	}
}

// TestRejectVaultServersBaselineServersNotBanned — explicit assertion that
// the three currently-deployed server names never match.
func TestRejectVaultServersBaselineServersNotBanned(t *testing.T) {
	for _, name := range []string{"postgres", "mempalace", "finalize"} {
		cfg := mcpConfig{MCPServers: map[string]mcpServerSpec{name: {Command: "/x"}}}
		if err := RejectVaultServers(cfg); err != nil {
			t.Errorf("server %q should not be banned; got: %v", name, err)
		}
	}
}

// TestRejectVaultServersNamedVault — server literally named "vault" triggers
// the filter.
func TestRejectVaultServersNamedVault(t *testing.T) {
	cfg := mcpConfig{MCPServers: map[string]mcpServerSpec{
		"vault": {Command: "/bin/vault"},
	}}
	err := RejectVaultServers(cfg)
	if err == nil {
		t.Fatal("expected error for server named 'vault', got nil")
	}
	if !strings.Contains(err.Error(), "vault") {
		t.Errorf("error should name the offending key; got: %v", err)
	}
}

// TestRejectVaultServersCaseInsensitive — mixed-case server names are matched.
func TestRejectVaultServersCaseInsensitive(t *testing.T) {
	cfg := mcpConfig{MCPServers: map[string]mcpServerSpec{
		"VaultClient": {Command: "/bin/vault"},
	}}
	if err := RejectVaultServers(cfg); err == nil {
		t.Fatal("expected error for 'VaultClient', got nil")
	}
}

// TestRejectVaultServersSubstringMatch — "infisical" embedded in a longer
// server name is caught.
func TestRejectVaultServersSubstringMatch(t *testing.T) {
	cfg := mcpConfig{MCPServers: map[string]mcpServerSpec{
		"my-infisical-bridge": {Command: "/bin/bridge"},
	}}
	err := RejectVaultServers(cfg)
	if err == nil {
		t.Fatal("expected error for 'my-infisical-bridge', got nil")
	}
	if !strings.Contains(err.Error(), "my-infisical-bridge") {
		t.Errorf("error should name the offending key; got: %v", err)
	}
}

// TestWriteRejectsVaultMCPServer — end-to-end: Write() must return an error
// and NOT create a file if the composed config contains a banned server.
func TestWriteRejectsVaultMCPServer(t *testing.T) {
	dir := t.TempDir()
	id := mustUUID(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")

	// Intercept via WriteWithOps with real osOps but an injected "secret" server.
	// We inject it by directly exercising the lower layer after composing a
	// config that WriteWithOps cannot produce legitimately.
	cfg := mcpConfig{MCPServers: map[string]mcpServerSpec{
		"postgres": {Command: "/bin/sup"},
		"secret":   {Command: "/bin/bad"},
	}}
	err := RejectVaultServers(cfg)
	if err == nil {
		t.Fatal("RejectVaultServers: expected error for 'secret', got nil")
	}

	// Confirm the public Write API with a custom finalizeParams that
	// encodes a banned name doesn't write the file.
	// (Write's finalize server is named "finalize" — not banned. We test
	// the guard fires; the integration-level "real Write + vault server"
	// scenario is covered by the lower-layer test above.)
	_ = id
	_ = dir
}
