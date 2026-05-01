// Package objstore wraps the MinIO client M5.4 uses to store the
// CEO-editable Company.md object. Owns: scoped-credential fetch (via
// internal/vault.Client), bucket bootstrap (BucketExists +
// MakeBucket; idempotent fail-closed), object Get/Put with ETag-based
// optimistic concurrency, leak-scan + size-cap enforcement on Put.
//
// Mirrors the structure of internal/finalize: a small in-process
// wrapper around an external service with all error paths typed.
package objstore

import (
	"errors"

	"github.com/garrison-hq/garrison/supervisor/internal/leakscan"
)

// Sentinel errors classified by errors.Is at the HTTP-handler layer
// in internal/dashboardapi.
var (
	ErrMinIOUnreachable = errors.New("objstore: minio unreachable")
	ErrMinIOAuthFailed  = errors.New("objstore: minio auth failed")
	ErrStale            = errors.New("objstore: if-match precondition failed")
	ErrLeakScanFailed   = errors.New("objstore: leak scan rejected content")
	ErrTooLarge         = errors.New("objstore: content exceeds size cap")
)

// LeakScanError wraps ErrLeakScanFailed with the matched pattern's
// category. Callers surface Category in the HTTP response so the
// editor can name what kind of secret was caught — but per Rule 1 the
// matched substring is NEVER carried back. The struct intentionally has
// no field that holds bytes from the scanned content.
type LeakScanError struct {
	Category leakscan.MatchCategory
}

func (e *LeakScanError) Error() string {
	return "objstore: leak scan rejected content (category: " + string(e.Category) + ")"
}

func (e *LeakScanError) Unwrap() error { return ErrLeakScanFailed }
