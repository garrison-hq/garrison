package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"

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
	dbURL := os.Getenv("ORG_OS_DATABASE_URL")
	if dbURL == "" {
		fmt.Fprintln(os.Stderr, "supervisor: --migrate requires ORG_OS_DATABASE_URL")
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
	return ExitOK
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
