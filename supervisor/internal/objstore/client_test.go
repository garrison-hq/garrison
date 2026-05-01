package objstore_test

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

// Unit-level coverage of Client construction-time validation. Full
// happy-path Get/Put/BootstrapBucket coverage lives in
// integration_test.go (//go:build integration) which boots a real
// MinIO testcontainer per spike §F4.

func TestNew_RejectsMissingEndpoint(t *testing.T) {
	_, err := objstore.New(objstore.Config{
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "garrison-company",
		CompanyID: "00000000-0000-0000-0000-000000000001",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected error for empty endpoint")
	}
}

func TestNew_RejectsMissingCredentials(t *testing.T) {
	_, err := objstore.New(objstore.Config{
		Endpoint:  "garrison-minio:9000",
		Bucket:    "garrison-company",
		CompanyID: "00000000-0000-0000-0000-000000000001",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected error for empty access/secret key")
	}
}

func TestNew_RejectsMissingBucket(t *testing.T) {
	_, err := objstore.New(objstore.Config{
		Endpoint:  "garrison-minio:9000",
		AccessKey: "a",
		SecretKey: "s",
		CompanyID: "00000000-0000-0000-0000-000000000001",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected error for empty bucket")
	}
}

func TestNew_RejectsMissingCompanyID(t *testing.T) {
	_, err := objstore.New(objstore.Config{
		Endpoint:  "garrison-minio:9000",
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "garrison-company",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		t.Fatal("expected error for empty companyID")
	}
}

func TestNew_HappyPath(t *testing.T) {
	c, err := objstore.New(objstore.Config{
		Endpoint:  "garrison-minio:9000",
		AccessKey: "a",
		SecretKey: "s",
		Bucket:    "garrison-company",
		CompanyID: "00000000-0000-0000-0000-000000000001",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("expected clean construction; got %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil Client")
	}
}

// TestErrors_StaleIs verifies the sentinel hierarchy our HTTP layer
// classifies against.
func TestErrors_StaleIs(t *testing.T) {
	if !errors.Is(objstore.ErrStale, objstore.ErrStale) {
		t.Error("ErrStale should be self-Is")
	}
	if errors.Is(objstore.ErrStale, objstore.ErrTooLarge) {
		t.Error("ErrStale must NOT be ErrTooLarge")
	}
}
