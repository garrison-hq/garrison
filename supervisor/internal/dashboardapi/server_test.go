package dashboardapi

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/config"
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
