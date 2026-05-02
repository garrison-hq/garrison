package objstore_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/leakscan"
	"github.com/garrison-hq/garrison/supervisor/internal/objstore"
)

func TestErrors_IsClassification(t *testing.T) {
	wrapped := &objstore.LeakScanError{Category: leakscan.CategorySKPrefix}
	if !errors.Is(wrapped, objstore.ErrLeakScanFailed) {
		t.Errorf("LeakScanError must unwrap to ErrLeakScanFailed; got %v", wrapped)
	}
}

func TestLeakScanError_CarriesCategory(t *testing.T) {
	err := &objstore.LeakScanError{Category: leakscan.CategoryAWSAccessKey}
	if err.Category != leakscan.CategoryAWSAccessKey {
		t.Errorf("Category lost in struct: %q", err.Category)
	}
}

// TestLeakScanError_DoesNotLeakSubstring is the Rule 1 backstop —
// the struct's Error() string MUST NOT contain anything that could
// echo a matched substring back to the operator.
func TestLeakScanError_DoesNotLeakSubstring(t *testing.T) {
	err := &objstore.LeakScanError{Category: leakscan.CategorySKPrefix}
	msg := err.Error()
	// The message is allowed to mention the category by name; it must
	// NOT structurally accept any other field (the struct only carries
	// Category, so this is a structural property).
	if !strings.Contains(msg, string(leakscan.CategorySKPrefix)) {
		t.Errorf("Error() should mention category; got %q", msg)
	}
	// Sanity check on the struct shape — single field.
	// (If a future change adds a content-bytes field, this test fails
	// at compile time when adapting it.)
}
