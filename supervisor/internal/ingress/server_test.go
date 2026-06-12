package ingress

// server_test.go — T017 coverage top-up tests for ingress.Server lifecycle
// (NewServer + Serve). The integration suite (T013) exercises the handler path
// via httptest.Server; these unit tests cover the Server construction and
// shutdown paths that T013 cannot reach without a real vault (NewServer's
// vault-fetch guard, nil-vault guard, missing-key guard) and the Serve
// goroutine lifecycle (start + graceful shutdown).
//
// Per tasks.md T017 rubric: "top up tests if short — no production-code changes."
// Only test files are added here; server.go is unchanged.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/vault"
	"github.com/jackc/pgx/v5/pgtype"
)

// ---------------------------------------------------------------------------
// Fake vault.Fetcher implementations
// ---------------------------------------------------------------------------

// failFetcher implements vault.Fetcher and always returns an error.
type failFetcher struct{ err error }

func (f failFetcher) Fetch(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
	return nil, f.err
}

// emptyFetcher implements vault.Fetcher and returns an empty map (no keys).
type emptyFetcher struct{}

func (emptyFetcher) Fetch(_ context.Context, _ []vault.GrantRow) (map[string]vault.SecretValue, error) {
	return map[string]vault.SecretValue{}, nil
}

// successFetcher implements vault.Fetcher and returns a pre-baked secret
// for the webhook secret env key.
type successFetcher struct {
	key   string
	value vault.SecretValue
}

func (f successFetcher) Fetch(_ context.Context, reqs []vault.GrantRow) (map[string]vault.SecretValue, error) {
	result := make(map[string]vault.SecretValue, len(reqs))
	for _, r := range reqs {
		if r.EnvVarName == f.key {
			result[r.EnvVarName] = f.value
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Config builder helpers
// ---------------------------------------------------------------------------

// ingressCfg returns a minimal config.Config for ingress server unit tests.
// Port is set to a free OS-assigned port to avoid collisions in parallel
// test runs. ShutdownGrace is short so Serve tests don't wait long.
func ingressCfg(t *testing.T) *config.Config {
	t.Helper()
	port := findFreeIngressPort(t)
	return &config.Config{
		IngressPort:              int(port),
		IngressGitHubEnabled:     true,
		IngressGitHubConnectorID: "test-connector",
		IngressGitHubDepartment:  "engineering",
		IngressGitHubRatePerMin:  60,
		IngressGitHubBurst:       30,
		ShutdownGrace:            200 * time.Millisecond,
	}
}

// findFreeIngressPort allocates and immediately releases a TCP port for the
// test. Mirrors findFreePort in dashboardapi/server_test.go.
func findFreeIngressPort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return port
}

// ingressDeps returns a minimal Deps for unit tests. VaultClient is intentionally
// NOT set here — tests set it directly to exercise the specific guard they need.
func ingressDeps() Deps {
	var counter atomic.Int64
	return Deps{
		Pool:             nil, // handler never called in lifecycle tests
		Queries:          nil,
		VaultClient:      nil, // intentionally nil — override in each test
		CustomerID:       pgtype.UUID{Valid: true},
		RejectionCounter: &counter,
	}
}

// ---------------------------------------------------------------------------
// TestNewServer_NilVaultClient_ReturnsError — FR-302: a nil vault client
// causes NewServer to return an error immediately (fail-closed).
// ---------------------------------------------------------------------------
func TestNewServer_NilVaultClient_ReturnsError(t *testing.T) {
	cfg := ingressCfg(t)
	deps := ingressDeps()
	deps.VaultClient = nil // explicit nil

	_, err := NewServer(context.Background(), cfg, deps, nil)
	if err == nil {
		t.Fatal("NewServer with nil VaultClient returned nil error; want error (FR-302 fail-closed)")
	}
	t.Logf("got expected error: %v", err)
}

// TestNewServer_VaultFetchError_ReturnsError — FR-302: a vault.Fetcher that
// returns an error causes NewServer to fail closed.
func TestNewServer_VaultFetchError_ReturnsError(t *testing.T) {
	cfg := ingressCfg(t)
	deps := ingressDeps()
	deps.VaultClient = failFetcher{err: errors.New("infisical: connection refused")}

	_, err := NewServer(context.Background(), cfg, deps, nil)
	if err == nil {
		t.Fatal("NewServer with failing vault returned nil error; want error (FR-302 fail-closed)")
	}
	t.Logf("got expected error: %v", err)
}

// TestNewServer_VaultKeyMissing_ReturnsError — FR-302: vault returns an empty
// map (key absent) → NewServer fails closed.
func TestNewServer_VaultKeyMissing_ReturnsError(t *testing.T) {
	cfg := ingressCfg(t)
	deps := ingressDeps()
	deps.VaultClient = emptyFetcher{} // returns no keys

	_, err := NewServer(context.Background(), cfg, deps, nil)
	if err == nil {
		t.Fatal("NewServer with missing vault key returned nil error; want error (FR-302 fail-closed)")
	}
	t.Logf("got expected error: %v", err)
}

// TestNewServer_ValidVault_ReturnsServer — happy path: vault returns the
// webhook secret → NewServer returns a non-nil Server with no error.
func TestNewServer_ValidVault_ReturnsServer(t *testing.T) {
	cfg := ingressCfg(t)
	deps := ingressDeps()
	deps.VaultClient = successFetcher{
		key:   webhookSecretEnvKey,
		value: vault.New([]byte("test-webhook-secret-for-unit")),
	}

	srv, err := NewServer(context.Background(), cfg, deps, nil)
	if err != nil {
		t.Fatalf("NewServer with valid vault returned error: %v", err)
	}
	if srv == nil {
		t.Fatal("NewServer returned nil Server; want non-nil")
	}
}

// TestServe_StartsAndShutdown — Serve binds on the configured port, accepts
// requests, and returns nil after the context is cancelled (graceful shutdown).
// This exercises the goroutine lifecycle + context.WithoutCancel shutdown path
// in Serve (concurrency rule 6).
func TestServe_StartsAndShutdown(t *testing.T) {
	cfg := ingressCfg(t)
	deps := ingressDeps()
	deps.VaultClient = successFetcher{
		key:   webhookSecretEnvKey,
		value: vault.New([]byte("test-webhook-secret-serve")),
	}

	srv, err := NewServer(context.Background(), cfg, deps, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	// Poll until the port is accepting connections.
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.IngressPort)
	deadline := time.Now().Add(3 * time.Second)
	var dialErr error
	for time.Now().Before(deadline) {
		var conn net.Conn
		conn, dialErr = net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if dialErr != nil {
		cancel()
		t.Fatalf("ingress server never bound to %s: %v", addr, dialErr)
	}

	// Issue a GET request to the mux (the webhook handler only handles POST;
	// GET returns 405 but the connection is accepted — proving the server is up).
	url := "http://" + addr + "/webhook/github"
	resp, err := http.Get(url) //nolint:noctx — test-only
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", url, err)
	}
	resp.Body.Close()
	// 405 is expected because the mux pattern is "POST /webhook/github".
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Logf("status=%d (any non-error response proves the server is up)", resp.StatusCode)
	}

	// Cancel the context → Serve should shut down within shutdownGrace.
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("Serve returned non-nil error after ctx cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancel within 2s")
	}
}

// TestServe_ListenAndServeError — if the bind address is already in use,
// Serve returns the listen error (not http.ErrServerClosed).
// This exercises the errCh path in Serve when ListenAndServe fails immediately.
func TestServe_ListenAndServeError(t *testing.T) {
	// Occupy a port first.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer l.Close()
	port := uint16(l.Addr().(*net.TCPAddr).Port)

	cfg := &config.Config{
		IngressPort:              int(port),
		IngressGitHubConnectorID: "test-connector",
		IngressGitHubDepartment:  "engineering",
		IngressGitHubRatePerMin:  60,
		IngressGitHubBurst:       30,
		ShutdownGrace:            200 * time.Millisecond,
	}
	var counter atomic.Int64
	deps := Deps{
		CustomerID:       pgtype.UUID{Valid: true},
		RejectionCounter: &counter,
		VaultClient: successFetcher{
			key:   webhookSecretEnvKey,
			value: vault.New([]byte("test-webhook-secret-busy-port")),
		},
	}

	srv, err := NewServer(context.Background(), cfg, deps, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Serve must return an error because the port is occupied.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	select {
	case err := <-serveErr:
		if err == nil {
			t.Error("Serve returned nil; want bind error (address in use)")
		}
		t.Logf("got expected bind error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after bind failure")
	}
}
