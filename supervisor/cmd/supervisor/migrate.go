package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationsFS embeds the SQL files that `make copy-migrations` stages into
// supervisor/migrations/ from the repo-root migrations/ directory. The
// canonical source lives at the repo root so a future Drizzle-based
// dashboard can derive the same schema without a Go dep; this copy is a
// build-time artefact and is gitignored.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// runMigrate opens a database/sql handle backed by the pgx stdlib shim,
// points goose at the embedded migrations FS, and runs UpContext to bring
// the schema to head. Exits ExitOK on success, ExitMigrateFailed on any
// connection/SQL error per contracts/cli.md.
func runMigrate() int {
	dbURL := os.Getenv("GARRISON_DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "supervisor: --migrate requires GARRISON_DATABASE_URL")
		return ExitMigrateFailed
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	sqlDB, err := openMigrationDB(dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "supervisor: migrate: open db: %v\n", err)
		return ExitMigrateFailed
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(&gooseSlogAdapter{logger: logger})
	if err := goose.SetDialect("postgres"); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor: migrate: dialect: %v\n", err)
		return ExitMigrateFailed
	}

	if err := goose.UpContext(context.Background(), sqlDB, "migrations"); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor: migrate: up: %v\n", err)
		return ExitMigrateFailed
	}
	logger.Info("migrations applied")

	// Post-migration: apply GARRISON_AGENT_RO_PASSWORD to the garrison_agent_ro
	// role created in migration 20260422000003. The role is created with LOGIN
	// but no password; this step flips it to a usable credential. If the env
	// var is unset we leave the role password-less and log a warning — the
	// supervisor's runtime config validation will refuse to start without the
	// env var, so the failure layer is clear.
	if err := applyAgentROPassword(context.Background(), sqlDB, logger); err != nil {
		fmt.Fprintf(os.Stderr, "supervisor: migrate: set garrison_agent_ro password: %v\n", err)
		return ExitMigrateFailed
	}
	return ExitOK
}

// applyAgentROPassword sets the garrison_agent_ro role's password from
// GARRISON_AGENT_RO_PASSWORD if present.
//
// `ALTER ROLE ... PASSWORD` is DDL and does not accept a parameter
// placeholder in the password-literal position, so we cannot write
// `PASSWORD $1` directly. Rather than format the password into a Go
// string ourselves (which would be an SQL-injection shape even with
// quote-doubling), we bind the value to a transaction-local GUC via
// `set_config('garrison.new_agent_ro_pw', $1, true)` — which DOES
// accept parameter placeholders — and let Postgres's own
// `format('%L', ...)` perform the literal quoting server-side inside
// a DO block. The password value therefore never appears in any SQL
// string built on the Go side; it is bound as a pgx parameter and
// interpolated by Postgres's canonical literal-quoter. NUL bytes are
// still rejected up front for a clearer error than Postgres's own.
func applyAgentROPassword(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	password := os.Getenv("GARRISON_AGENT_RO_PASSWORD")
	if password == "" {
		logger.Warn("GARRISON_AGENT_RO_PASSWORD is unset; garrison_agent_ro role has no password and cannot authenticate. Set the env var and re-run --migrate.")
		return nil
	}
	if strings.ContainsRune(password, 0) {
		return fmt.Errorf("password contains NUL byte; rejected")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		"SELECT set_config('garrison.new_agent_ro_pw', $1, true)", password); err != nil {
		return fmt.Errorf("bind password: %w", err)
	}
	const alterRoleDO = `DO $body$
BEGIN
  EXECUTE format(
    'ALTER ROLE garrison_agent_ro WITH LOGIN PASSWORD %L',
    current_setting('garrison.new_agent_ro_pw')
  );
END
$body$`
	if _, err := tx.ExecContext(ctx, alterRoleDO); err != nil {
		return fmt.Errorf("alter role: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	logger.Info("garrison_agent_ro password applied")
	return nil
}

// openMigrationDB returns a *sql.DB backed by the pgx/v5 stdlib driver.
// The URL is parsed through pgxpool.ParseConfig so any pgx-specific URL
// params (sslmode, search_path, etc.) are honoured identically to the
// daemon's connection path.
func openMigrationDB(url string) (*sql.DB, error) {
	poolCfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	return stdlib.OpenDB(*poolCfg.ConnConfig), nil
}

// gooseSlogAdapter bridges goose's logger interface to slog so migration
// progress lands in the same JSON stream as the rest of the supervisor.
type gooseSlogAdapter struct{ logger *slog.Logger }

func (g *gooseSlogAdapter) Fatalf(format string, v ...any) {
	g.logger.Error(fmt.Sprintf(format, v...))
	os.Exit(ExitMigrateFailed)
}

func (g *gooseSlogAdapter) Printf(format string, v ...any) {
	g.logger.Info(fmt.Sprintf(format, v...))
}
