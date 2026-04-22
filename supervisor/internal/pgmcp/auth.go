package pgmcp

import (
	"fmt"
	"strings"
)

// allowReadOnly returns nil if sql is a bare SELECT or EXPLAIN statement
// (case-insensitive prefix match after whitespace and leading comments are
// stripped). Anything else returns an error suitable for the
// errCodeReadOnlyViolation response. This is the protocol-layer defense-
// in-depth on top of the garrison_agent_ro role's GRANT SELECT (NFR-104).
//
// The filter is intentionally a prefix check, not a SQL parser: the
// authoritative guarantee is the Postgres role, which will reject DML
// regardless of what the client says. The prefix filter is there to
// produce a nicer error message than a Postgres permission-denied, and
// to keep obvious mistakes out of the DB's log noise.
func allowReadOnly(sql string) error {
	prefix := firstKeyword(sql)
	switch strings.ToUpper(prefix) {
	case "SELECT", "EXPLAIN", "WITH", "SHOW", "VALUES":
		return nil
	}
	return fmt.Errorf("read-only violation: first keyword %q is not SELECT/EXPLAIN/WITH/SHOW/VALUES", prefix)
}

// allowSelectOnly is stricter: only SELECT (used by the explain tool
// which composes EXPLAIN <sql> itself).
func allowSelectOnly(sql string) error {
	prefix := firstKeyword(sql)
	if strings.EqualFold(prefix, "SELECT") {
		return nil
	}
	return fmt.Errorf("explain requires a SELECT statement; got first keyword %q", prefix)
}

// firstKeyword strips leading whitespace and SQL line comments, then
// returns the first whitespace-or-paren-delimited token.
func firstKeyword(sql string) string {
	s := sql
	for {
		s = strings.TrimLeft(s, " \t\n\r")
		if strings.HasPrefix(s, "--") {
			if nl := strings.IndexByte(s, '\n'); nl >= 0 {
				s = s[nl+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if end := strings.Index(s, "*/"); end >= 0 {
				s = s[end+2:]
				continue
			}
			return ""
		}
		break
	}
	if idx := strings.IndexAny(s, " \t\n\r("); idx > 0 {
		return s[:idx]
	}
	return s
}
