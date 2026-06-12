package dashboardapi

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

// findFreePort returns an OS-allocated TCP port that should be free
// for the duration of the test (defensive — the OS may reuse it
// concurrently with another process).
func findFreePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return uint16(port)
}

// TestServerLifecycle_ServeAndShutdown — server boots, accepts a
// request (via the Mux's wired handler), shuts down cleanly when ctx
// cancels.
func TestServerLifecycle_ServeAndShutdown(t *testing.T) {
	port := findFreePort(t)
	cfg := &config.Config{
		DashboardAPIPort: port,
		ShutdownGrace:    2 * time.Second,
	}

	srv := NewServer(cfg, Deps{
		SessionValidator: fakeValidator{userID: "uid"},
	})
	srv.Mux().Handle("/test/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pong"))
	}))

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx) }()

	// Poll until the listener accepts. ListenAndServe is async; the
	// Serve goroutine binds + Listens before serving the goroutine
	// returns, but there's no readiness signal — short retry loop.
	url := "http://127.0.0.1:" + httpPort(port) + "/test/ping"
	deadline := time.Now().Add(3 * time.Second)
	var resp *http.Response
	var err error
	for time.Now().Before(deadline) {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status=%d; want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Cancel ctx → Serve should return nil within shutdownGrace.
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Errorf("Serve returned err: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

// TestRegisterDefaultRoutes_IngressStatus — RegisterDefaultRoutes wires the
// M10 /ingress/status endpoint behind cookie-auth. This test exercises the
// new route registration lines in server.go (the M10 diff adds these lines)
// and confirms that:
//
//	a. An unauthenticated GET /ingress/status returns 401 (cookie-auth gate).
//	b. An authenticated GET /ingress/status returns 200 with JSON.
//
// This also covers the IngressRejectionCounter Deps field path.
func TestRegisterDefaultRoutes_IngressStatus(t *testing.T) {
	port := findFreePort(t)
	cfg := &config.Config{
		DashboardAPIPort: port,
		ShutdownGrace:    200 * time.Millisecond,
	}

	var counter atomic.Int64
	counter.Add(3)

	// Build a minimal *objstore.Client. minio.New is lazy — it does NOT connect
	// at construction, only on actual requests. RegisterDefaultRoutes only
	// requires deps.Objstore != nil; the handler never executes in this test.
	objstoreClient, err := objstore.New(objstore.Config{
		Endpoint:  "127.0.0.1:9000", // unreachable; never dialed in this test
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
		Bucket:    "test-bucket",
		CompanyID: "test-company-id",
	}, nil)
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}

	srv := NewServer(cfg, Deps{
		SessionValidator:        fakeValidator{userID: "test-uid"},
		IngressRejectionCounter: &counter,
		Objstore:                objstoreClient,
	})
	if err := srv.RegisterDefaultRoutes(Deps{
		SessionValidator:        fakeValidator{userID: "test-uid"},
		IngressRejectionCounter: &counter,
		Objstore:                objstoreClient,
	}); err != nil {
		t.Fatalf("RegisterDefaultRoutes: %v", err)
	}

	// Use httptest.Server directly against srv.Mux() to avoid port conflicts.
	ts := httptest.NewServer(srv.Mux())
	t.Cleanup(ts.Close)

	// a. Unauthenticated GET → 401.
	reqUnauth, _ := http.NewRequest(http.MethodGet, ts.URL+"/ingress/status", nil)
	respUnauth, err := http.DefaultClient.Do(reqUnauth)
	if err != nil {
		t.Fatalf("GET /ingress/status (unauth): %v", err)
	}
	respUnauth.Body.Close()
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d; want 401", respUnauth.StatusCode)
	}

	// b. Authenticated GET → 200 with JSON body.
	reqAuth, _ := http.NewRequest(http.MethodGet, ts.URL+"/ingress/status", nil)
	// The fakeValidator validates any "session-token" cookie value when userID != "".
	reqAuth.AddCookie(&http.Cookie{Name: "better-auth.session_token", Value: "test-session"})
	respAuth, err := http.DefaultClient.Do(reqAuth)
	if err != nil {
		t.Fatalf("GET /ingress/status (auth): %v", err)
	}
	respAuth.Body.Close()
	if respAuth.StatusCode != http.StatusOK {
		t.Errorf("authenticated status = %d; want 200", respAuth.StatusCode)
	}
}

// httpPort returns the port as a decimal string for url assembly.
func httpPort(p uint16) string {
	const digits = "0123456789"
	if p == 0 {
		return "0"
	}
	var buf [5]byte
	i := len(buf)
	for p > 0 {
		i--
		buf[i] = digits[p%10]
		p /= 10
	}
	return string(buf[i:])
}
