package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const usage = `Usage: supervisor [FLAGS]

Garrison supervisor daemon. Listens on Postgres pg_notify and spawns
agent subprocesses on work.ticket.created events.

Flags:
  --version         Print version and exit.
  --help, -h        Print this message and exit.

Environment variables (daemon mode):
  ORG_OS_DATABASE_URL        required   Postgres connection URL.
  ORG_OS_FAKE_AGENT_CMD      required   Command template for the fake agent.
  ORG_OS_POLL_INTERVAL       5s         Fallback poll interval (min 1s).
  ORG_OS_SUBPROCESS_TIMEOUT  60s        Per-subprocess timeout.
  ORG_OS_SHUTDOWN_GRACE      30s        Graceful shutdown deadline.
  ORG_OS_HEALTH_PORT         8080       HTTP health server port.
  ORG_OS_LOG_LEVEL           info       Log level (debug|info|warn|error).
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("supervisor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stdout, usage) }

	showVersion := fs.Bool("version", false, "print version and exit")
	showHelp := fs.Bool("help", false, "print help and exit")
	fs.BoolVar(showHelp, "h", false, "shorthand for --help")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return 0
		}
		return 2
	}

	if *showHelp {
		fs.Usage()
		return 0
	}
	if *showVersion {
		fmt.Fprintf(os.Stdout, "supervisor %s\n", version)
		return 0
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "supervisor: unexpected positional arguments: %v\n", fs.Args())
		fs.Usage()
		return 2
	}

	fmt.Fprintln(os.Stderr, "supervisor: daemon mode not yet implemented (see T012)")
	return 1
}
