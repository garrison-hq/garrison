package objstore

import "github.com/garrison-hq/garrison/supervisor/internal/leakscan"

// Scan delegates to internal/leakscan.Scan against the canonical
// 10-pattern set (M2.3 Rule 1 carryover). Returns ("", nil) for clean
// content; returns a wrapped *LeakScanError with the matched
// category for any hit. The first match wins so the operator-facing
// rejection message names exactly one pattern category.
func Scan(content []byte) error {
	cat := leakscan.Scan(content)
	if cat == "" {
		return nil
	}
	return &LeakScanError{Category: cat}
}
