package pgmcp

import "testing"

// TestAllowReadOnlyAcceptsSelectFamily pins the positive set — bare
// SELECT, EXPLAIN, WITH (CTE), SHOW, and VALUES are all read-only.
func TestAllowReadOnlyAcceptsSelectFamily(t *testing.T) {
	cases := []string{
		"SELECT 1",
		"  SELECT * FROM tickets",
		"select id from agents",
		"EXPLAIN SELECT 1",
		"WITH recent AS (SELECT 1) SELECT * FROM recent",
		"SHOW server_version",
		"VALUES (1,2),(3,4)",
		"-- leading line comment\nSELECT 1",
		"/* block comment */ SELECT 1",
	}
	for _, sql := range cases {
		if err := allowReadOnly(sql); err != nil {
			t.Errorf("allowReadOnly(%q) = %v, want nil", sql, err)
		}
	}
}

// TestAllowReadOnlyRejectsDML pins the negative set — INSERT, UPDATE,
// DELETE, DDL, and transaction-control statements are all rejected.
func TestAllowReadOnlyRejectsDML(t *testing.T) {
	cases := []string{
		"INSERT INTO tickets (id) VALUES (1)",
		"UPDATE tickets SET column_slug = 'done'",
		"DELETE FROM tickets",
		"DROP TABLE tickets",
		"TRUNCATE tickets",
		"CREATE TABLE foo (id INT)",
		"ALTER TABLE tickets ADD COLUMN x INT",
		"GRANT SELECT ON tickets TO garrison_agent_ro",
		"BEGIN",
		"COMMIT",
		"ROLLBACK",
	}
	for _, sql := range cases {
		if err := allowReadOnly(sql); err == nil {
			t.Errorf("allowReadOnly(%q) = nil, want error", sql)
		}
	}
}

// TestAllowSelectOnlyIsStricter pins that the explain-tool filter
// rejects EXPLAIN itself (the server composes EXPLAIN <sql> so the
// client must pass a bare SELECT).
func TestAllowSelectOnlyIsStricter(t *testing.T) {
	if err := allowSelectOnly("SELECT 1"); err != nil {
		t.Errorf("allowSelectOnly(SELECT 1) = %v, want nil", err)
	}
	if err := allowSelectOnly("EXPLAIN SELECT 1"); err == nil {
		t.Errorf("allowSelectOnly(EXPLAIN SELECT 1) = nil, want error")
	}
	if err := allowSelectOnly("INSERT INTO x VALUES (1)"); err == nil {
		t.Errorf("allowSelectOnly(INSERT) = nil, want error")
	}
}

// TestFirstKeywordSkipsCommentsAndWhitespace pins the parser helper so a
// comment or leading whitespace does not defeat the filter.
func TestFirstKeywordSkipsCommentsAndWhitespace(t *testing.T) {
	cases := []struct{ sql, want string }{
		{"SELECT 1", "SELECT"},
		{"  SELECT 1", "SELECT"},
		{"\n\tselect 1", "select"},
		{"-- x\nSELECT 1", "SELECT"},
		{"/* x */ SELECT 1", "SELECT"},
		{"-- only comment", ""},
		{"", ""},
		{"INSERT INTO x VALUES (1)", "INSERT"},
	}
	for _, c := range cases {
		if got := firstKeyword(c.sql); got != c.want {
			t.Errorf("firstKeyword(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}

// TestFirstWordBasics pins the log-line helper's behaviour on the shapes
// that actually show up in practice. Leading `(` at index 0 is treated as
// part of the token (there's nothing before it to split on); otherwise
// firstWord splits at the first whitespace or `(`.
func TestFirstWordBasics(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1 FROM t", "SELECT"},
		{"SELECT", "SELECT"},
		{"  SELECT  1", "SELECT"},
		{"INSERT INTO x VALUES (1)", "INSERT"},
		{"count(*)", "count"},
	}
	for _, c := range cases {
		if got := firstWord(c.in); got != c.want {
			t.Errorf("firstWord(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
