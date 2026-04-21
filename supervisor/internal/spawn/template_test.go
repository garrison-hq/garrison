package spawn_test

import (
	"context"
	"strings"
	"testing"

	"github.com/garrison-hq/garrison/supervisor/internal/spawn"
)

const (
	testTicketID     = "11111111-1111-1111-1111-111111111111"
	testDepartmentID = "22222222-2222-2222-2222-222222222222"
)

func TestSubstituteLiteralTokens(t *testing.T) {
	raw := `/bin/echo ticket=$TICKET_ID dept=$DEPARTMENT_ID`

	cmd, err := spawn.BuildCommand(context.Background(), raw, testTicketID, testDepartmentID)
	if err != nil {
		t.Fatalf("BuildCommand: unexpected error: %v", err)
	}
	if got, want := cmd.Args[0], "/bin/echo"; got != want {
		t.Errorf("Args[0] = %q, want %q", got, want)
	}
	if got, want := cmd.Args[1], "ticket="+testTicketID; got != want {
		t.Errorf("Args[1] = %q, want %q", got, want)
	}
	if got, want := cmd.Args[2], "dept="+testDepartmentID; got != want {
		t.Errorf("Args[2] = %q, want %q", got, want)
	}
}

func TestSubstituteAlsoSetsEnv(t *testing.T) {
	cmd, err := spawn.BuildCommand(context.Background(), "/bin/true", testTicketID, testDepartmentID)
	if err != nil {
		t.Fatalf("BuildCommand: unexpected error: %v", err)
	}

	var sawTicket, sawDept bool
	for _, e := range cmd.Env {
		if e == "TICKET_ID="+testTicketID {
			sawTicket = true
		}
		if e == "DEPARTMENT_ID="+testDepartmentID {
			sawDept = true
		}
	}
	if !sawTicket {
		t.Errorf("cmd.Env missing TICKET_ID=%s (got %v)", testTicketID, cmd.Env)
	}
	if !sawDept {
		t.Errorf("cmd.Env missing DEPARTMENT_ID=%s (got %v)", testDepartmentID, cmd.Env)
	}
}

func TestShlexRejectsUnterminatedQuote(t *testing.T) {
	// shlex must reject malformed input rather than silently producing a
	// degenerate argv; surfacing the typo at BuildCommand time (and thus at
	// supervisor startup, once T009 threads the call in) is strictly better
	// than a spawn failure on the first event.
	raw := `/bin/echo "unterminated`

	_, err := spawn.BuildCommand(context.Background(), raw, testTicketID, testDepartmentID)
	if err == nil {
		t.Fatalf("BuildCommand: want error for unterminated quote, got nil")
	}
	if !strings.Contains(err.Error(), "shlex") {
		t.Errorf("err = %q, want error mentioning shlex", err.Error())
	}
}
