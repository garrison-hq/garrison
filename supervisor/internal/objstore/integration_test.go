//go:build integration

// Integration tests for the objstore Client against a real MinIO
// container booted via testcontainers-go. testcontainers-go has no
// built-in MinIO module so we use the generic-container API per spike
// §F2 with image digest pinned per §F1.

package objstore_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// Pinned by digest (matches docker-compose.yml + the dashboard
	// Playwright job's Pre-pull step in .github/workflows/ci.yml).
	// Per spike §F1.
	testMinIOImage      = "minio/minio@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e"
	testMinIORootUser   = "spike-user"
	testMinIORootPasswd = "spike-password-123"
	testCompanyID       = "00000000-0000-0000-0000-000000000001"
)

// startMinIO boots a single-node MinIO container, exposes port 9000,
// and returns an objstore.Client wired against it. Test cleanup
// terminates the container.
func startMinIO(t *testing.T) (*objstore.Client, testcontainers.Container) {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        testMinIOImage,
		ExposedPorts: []string{"9000/tcp"},
		Cmd:          []string{"server", "/data"},
		Env: map[string]string{
			"MINIO_ROOT_USER":     testMinIORootUser,
			"MINIO_ROOT_PASSWORD": testMinIORootPasswd,
		},
		WaitingFor: wait.ForHTTP("/minio/health/live").
			WithPort("9000/tcp").
			WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start MinIO: %v", err)
	}
	t.Cleanup(func() {
		ctxStop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = container.Terminate(ctxStop)
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	endpoint := host + ":" + port.Port()

	c, err := objstore.New(objstore.Config{
		Endpoint:  endpoint,
		UseTLS:    false,
		AccessKey: testMinIORootUser,
		SecretKey: testMinIORootPasswd,
		Bucket:    "garrison-company",
		CompanyID: testCompanyID,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("objstore.New: %v", err)
	}
	return c, container
}

// TestIntegration_BootstrapAgainstRealMinIO verifies the
// BucketExists+MakeBucket idempotent shape (spike §F5).
func TestIntegration_BootstrapAgainstRealMinIO(t *testing.T) {
	c, _ := startMinIO(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.BootstrapBucket(ctx); err != nil {
		t.Fatalf("first BootstrapBucket: %v", err)
	}
	if err := c.BootstrapBucket(ctx); err != nil {
		t.Fatalf("second BootstrapBucket should be idempotent: %v", err)
	}
}

// TestIntegration_GetMissingReturnsEmpty verifies the FR-624
// empty-state path: missing object returns (nil, "", nil).
func TestIntegration_GetMissingReturnsEmpty(t *testing.T) {
	c, _ := startMinIO(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.BootstrapBucket(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	content, etag, err := c.GetCompanyMD(ctx)
	if err != nil {
		t.Fatalf("GetCompanyMD missing: %v", err)
	}
	if content != nil || etag != "" {
		t.Errorf("expected (nil, \"\", nil) for missing object; got (%v, %q)", content, etag)
	}
}

// TestIntegration_GetPutRoundtripWithEtag verifies the ETag-based
// optimistic-concurrency contract (spike §F7).
func TestIntegration_GetPutRoundtripWithEtag(t *testing.T) {
	c, _ := startMinIO(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.BootstrapBucket(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// First save: empty etag (FR-624 empty-state path).
	etag, err := c.PutCompanyMD(ctx, []byte("# Garrison\n\nv1"), "")
	if err != nil {
		t.Fatalf("first PutCompanyMD: %v", err)
	}
	if etag == "" {
		t.Fatal("expected non-empty etag")
	}

	// Read back; etag round-trips.
	content, getEtag, err := c.GetCompanyMD(ctx)
	if err != nil {
		t.Fatalf("GetCompanyMD: %v", err)
	}
	if string(content) != "# Garrison\n\nv1" {
		t.Errorf("content roundtrip = %q; want '# Garrison\\n\\nv1'", content)
	}
	if getEtag != etag {
		t.Errorf("etag mismatch: put=%q get=%q", etag, getEtag)
	}

	// Second save with stale etag → ErrStale.
	if _, err := c.PutCompanyMD(ctx, []byte("v2"), "stale-etag"); !errors.Is(err, objstore.ErrStale) {
		t.Errorf("expected ErrStale on stale etag; got %v", err)
	}

	// Second save with current etag → succeeds with new etag.
	newEtag, err := c.PutCompanyMD(ctx, []byte("v2-final"), getEtag)
	if err != nil {
		t.Fatalf("second PutCompanyMD with current etag: %v", err)
	}
	if newEtag == getEtag {
		t.Errorf("expected new etag; got same: %q", newEtag)
	}
}

// TestIntegration_PutRejectsLeakBeforeMinIO verifies the leak-scan
// fires BEFORE the MinIO call (no object lands; subsequent Get
// returns empty).
func TestIntegration_PutRejectsLeakBeforeMinIO(t *testing.T) {
	c, _ := startMinIO(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.BootstrapBucket(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	_, err := c.PutCompanyMD(ctx, []byte("leak: sk-abcdefghij1234567890abcdef"), "")
	if !errors.Is(err, objstore.ErrLeakScanFailed) {
		t.Errorf("expected ErrLeakScanFailed; got %v", err)
	}

	// Verify nothing landed.
	content, _, err := c.GetCompanyMD(ctx)
	if err != nil {
		t.Fatalf("GetCompanyMD after leak-rejected put: %v", err)
	}
	if content != nil {
		t.Errorf("object should not have landed; got %q", content)
	}
}
