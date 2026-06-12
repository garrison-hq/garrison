package actionbroker

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestClassifyFloorIsApprove verifies that a floor action type returns
// TierApprove with the floor reason string (US3 #1 / FR-014 / SC-003).
// This is the primary layer-1 classification check.
func TestClassifyFloorIsApprove(t *testing.T) {
	tier, reason := Classify("github_issue_comment")
	if tier != TierApprove {
		t.Errorf("Classify(github_issue_comment) tier = %q; want %q", tier, TierApprove)
	}
	if !strings.Contains(reason, "permanent-Approve floor") {
		t.Errorf("Classify(github_issue_comment) reason = %q; want it to contain %q",
			reason, "permanent-Approve floor")
	}
}

// TestFloorCannotBeLowered verifies that even when the policy map has a
// floor action type set to a lower tier (contrived misconfiguration),
// Classify still returns TierApprove (floor wins — SC-003 / US3 #2 /
// plan D5a).
//
// This is the critical defence-in-depth test: it proves the floor is
// consulted BEFORE the policy map so no policy entry can override it.
func TestFloorCannotBeLowered(t *testing.T) {
	// Contrived misconfiguration: set a floor action type to TierAuto in
	// the policy map. A correctly-implemented Classify ignores this because
	// it checks the floor first.
	original, existed := policy["github_issue_comment"]
	policy["github_issue_comment"] = TierAuto
	t.Cleanup(func() {
		if existed {
			policy["github_issue_comment"] = original
		} else {
			delete(policy, "github_issue_comment")
		}
	})

	tier, reason := Classify("github_issue_comment")
	if tier != TierApprove {
		t.Errorf("Classify(github_issue_comment) with policy[github_issue_comment]=auto returned %q; "+
			"want %q — floor must win over policy (SC-003)", tier, TierApprove)
	}
	if !strings.Contains(reason, "permanent-Approve floor") {
		t.Errorf("reason = %q; want it to mention the permanent-Approve floor (not the policy path)", reason)
	}
}

// TestClassifyUnknownDefaultsApprove verifies that an action type with
// no classification in either the floor or the policy map defaults to
// TierApprove (FR-015 / US3 #4 / plan D6). This ensures a newly-added
// action type is never silently auto/notify.
func TestClassifyUnknownDefaultsApprove(t *testing.T) {
	tier, reason := Classify("never_registered")
	if tier != TierApprove {
		t.Errorf("Classify(never_registered) tier = %q; want %q", tier, TierApprove)
	}
	if !strings.Contains(reason, "unclassified action type") {
		t.Errorf("Classify(never_registered) reason = %q; want it to mention unclassified", reason)
	}
	if !strings.Contains(reason, "safe-by-construction default") {
		t.Errorf("Classify(never_registered) reason = %q; want it to mention safe-by-construction default", reason)
	}
}

// TestFloorCheckMatchesPolicy asserts that the action-type list in the
// pending_actions_floor_is_approve CHECK constraint in the M11 migration
// exactly matches the keys of the floor map used by Classify (plan D5c).
// This test prevents drift between the Go policy enforcement layer and
// the DB-level enforcement layer — both must enumerate the same floor
// action types or the dual-enforcement guarantee is broken (SC-003).
func TestFloorCheckMatchesPolicy(t *testing.T) {
	migrationPath := migrationFilePath(t)

	content, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("reading migration file %s: %v", migrationPath, err)
	}

	// Extract the action types from the NOT IN list in the
	// pending_actions_floor_is_approve CHECK constraint.
	// The constraint has the form:
	//   CHECK (action_type NOT IN ('github_issue_comment') OR tier = 'approve')
	// We extract the quoted strings inside NOT IN (...).
	checkTypes := extractFloorCheckTypes(t, string(content))

	// Collect the floor keys from the policy.
	floorKeys := FloorActionTypes()

	// Both sets must be identical (order-independent).
	sort.Strings(checkTypes)
	sort.Strings(floorKeys)

	if len(checkTypes) != len(floorKeys) {
		t.Fatalf("floor map has %d types (%v); migration CHECK has %d types (%v) — they must match (plan D5c)",
			len(floorKeys), floorKeys, len(checkTypes), checkTypes)
	}
	for i, ct := range checkTypes {
		if ct != floorKeys[i] {
			t.Errorf("mismatch at index %d: migration CHECK has %q, floor map has %q",
				i, ct, floorKeys[i])
		}
	}
}

// extractFloorCheckTypes parses the migration SQL and returns the action
// type strings listed in the pending_actions_floor_is_approve CHECK's
// NOT IN clause. It searches for the CONSTRAINT name and then extracts
// the NOT IN list directly from the surrounding SQL text.
func extractFloorCheckTypes(t *testing.T, sql string) []string {
	t.Helper()

	// Verify the CONSTRAINT block is present to give a meaningful error if not.
	if !strings.Contains(sql, "pending_actions_floor_is_approve") {
		t.Fatal("could not find CONSTRAINT pending_actions_floor_is_approve in migration SQL")
	}

	// Match the NOT IN list directly: action_type NOT IN ('a', 'b', ...).
	// The list is bounded by the first closing paren after NOT IN (.
	// Since action type strings are simple ASCII identifiers (no embedded
	// quotes or parens), this single-level paren match is sufficient.
	notInRe := regexp.MustCompile(`(?i)action_type\s+NOT\s+IN\s*\(([^)]+)\)`)
	nm := notInRe.FindStringSubmatch(sql)
	if nm == nil {
		t.Fatal("could not find action_type NOT IN (...) in migration SQL")
	}
	inList := nm[1]

	// Extract single-quoted strings.
	quotedRe := regexp.MustCompile(`'([^']+)'`)
	matches := quotedRe.FindAllStringSubmatch(inList, -1)
	if len(matches) == 0 {
		t.Fatalf("no quoted action types found in NOT IN list: %q", inList)
	}

	out := make([]string, 0, len(matches))
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}

// migrationFilePath returns the absolute path to the M11 migration SQL
// file, resolved relative to this test file's source location via
// runtime.Caller (the same approach used by internal/store tests).
func migrationFilePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// From supervisor/internal/actionbroker/ navigate to
	// supervisor/cmd/supervisor/migrations/
	return filepath.Join(
		filepath.Dir(thisFile),
		"..", "..", "cmd", "supervisor", "migrations",
		"20260612000001_m11_action_broker.sql",
	)
}
