// Package spawn owns the subprocess lifecycle for the fake agent. template.go
// holds the pure command-template parser used by Spawn (T009). Keeping this
// function pure — no I/O, no database, no subprocess side effects — lets the
// substitution rules be exercised in isolation without a Postgres or a live
// child process.
package spawn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/google/shlex"
)

// ErrEmptyCommand is returned when rawCmd parses to zero argv elements.
// Surfacing this as a typed error (rather than letting exec.Command panic on
// an empty name) makes the misconfiguration legible at startup.
var ErrEmptyCommand = errors.New("spawn: ORG_OS_FAKE_AGENT_CMD is empty after parsing")

// BuildCommand parses rawCmd as a POSIX-like shell command line via shlex,
// substitutes the literal tokens $TICKET_ID and $DEPARTMENT_ID in every argv
// element, and returns an *exec.Cmd whose environment also carries TICKET_ID
// and DEPARTMENT_ID. The dual mechanism — argv substitution AND env vars — is
// intentional per plan.md §"Subprocess lifecycle manager" step 3: argv is how
// the fake-agent shell script consumes the values in M1, env vars are how a
// real agent (Claude Code) will consume them post-M1.
//
// rawCmd comes straight from the ORG_OS_FAKE_AGENT_CMD env var. ticketID and
// departmentID are canonical-form UUID strings; the caller formats pgtype.UUID
// before invoking this function so template.go stays free of pgx and unit-
// testable without a database.
func BuildCommand(ctx context.Context, rawCmd, ticketID, departmentID string) (*exec.Cmd, error) {
	argv, err := shlex.Split(rawCmd)
	if err != nil {
		return nil, fmt.Errorf("spawn: shlex.Split: %w", err)
	}
	if len(argv) == 0 {
		return nil, ErrEmptyCommand
	}
	for i, a := range argv {
		a = strings.ReplaceAll(a, "$TICKET_ID", ticketID)
		a = strings.ReplaceAll(a, "$DEPARTMENT_ID", departmentID)
		argv[i] = a
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(),
		"TICKET_ID="+ticketID,
		"DEPARTMENT_ID="+departmentID,
	)
	return cmd, nil
}
