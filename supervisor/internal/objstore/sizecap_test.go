package objstore_test

import (
	"errors"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

func TestSizeCap_AcceptsAtBoundary(t *testing.T) {
	content := make([]byte, objstore.MaxCompanyMDBytes)
	if err := objstore.CheckSize(content); err != nil {
		t.Errorf("CheckSize(64KB) = %v; want nil", err)
	}
}

func TestSizeCap_RejectsOverBoundary(t *testing.T) {
	content := make([]byte, objstore.MaxCompanyMDBytes+1)
	err := objstore.CheckSize(content)
	if !errors.Is(err, objstore.ErrTooLarge) {
		t.Errorf("CheckSize(64KB+1) = %v; want ErrTooLarge", err)
	}
}

func TestSizeCap_AcceptsEmpty(t *testing.T) {
	if err := objstore.CheckSize(nil); err != nil {
		t.Errorf("CheckSize(nil) = %v; want nil", err)
	}
	if err := objstore.CheckSize([]byte{}); err != nil {
		t.Errorf("CheckSize(empty) = %v; want nil", err)
	}
}
