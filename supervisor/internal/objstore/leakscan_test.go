package objstore_test

import (
	"errors"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/leakscan"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

func TestScan_AcceptsClean(t *testing.T) {
	if err := objstore.Scan([]byte("clean operator-authored prose, no secrets")); err != nil {
		t.Errorf("Scan(clean) = %v; want nil", err)
	}
}

func TestScan_RejectsSecret(t *testing.T) {
	err := objstore.Scan([]byte("paste-fail: sk-abcdefghij1234567890abcdef in here"))
	if !errors.Is(err, objstore.ErrLeakScanFailed) {
		t.Fatalf("Scan(sk-) = %v; want ErrLeakScanFailed", err)
	}
	var lse *objstore.LeakScanError
	if !errors.As(err, &lse) {
		t.Fatalf("could not unwrap to *LeakScanError: %v", err)
	}
	if lse.Category != leakscan.CategorySKPrefix {
		t.Errorf("Category = %q; want sk-prefix", lse.Category)
	}
}

func TestScan_RejectsAWSKey(t *testing.T) {
	err := objstore.Scan([]byte("AKIA1234567890ABCDEF"))
	var lse *objstore.LeakScanError
	if !errors.As(err, &lse) {
		t.Fatalf("expected LeakScanError; got %v", err)
	}
	if lse.Category != leakscan.CategoryAWSAccessKey {
		t.Errorf("Category = %q; want aws-access-key", lse.Category)
	}
}
