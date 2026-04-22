.PHONY: build test test-integration test-chaos sqlc migrate run docker lint clean copy-migrations

# copy-migrations stages the repo-root migrations/ into cmd/supervisor/migrations/
# so `go:embed migrations/*.sql` in cmd/supervisor/migrate.go can find them
# (embed paths are relative to the source file's package). The staged copy
# is gitignored — the canonical source lives at the repo root so a future
# Drizzle-based dashboard can consume it without a Go dep.
copy-migrations:
	mkdir -p cmd/supervisor/migrations
	cp -r ../migrations/*.sql cmd/supervisor/migrations/ 2>/dev/null || true

build: copy-migrations
	CGO_ENABLED=0 go build -o bin/supervisor ./cmd/supervisor

test: copy-migrations
	go test ./...

test-integration: copy-migrations
	go test -tags=integration ./...

test-chaos: copy-migrations
	go test -tags=chaos ./...

sqlc:
	sqlc generate

migrate: build
	./bin/supervisor --migrate

run: build
	./bin/supervisor

docker: copy-migrations
	docker build -t garrison/supervisor:dev .

# copy-migrations is required because cmd/supervisor/migrate.go has a
# //go:embed directive that go vet and the compiler resolve at type-check
# time — without the staged files, `go vet ./...` fails.
lint: copy-migrations
	go vet ./...
	@if [ -n "$$(gofmt -l .)" ]; then gofmt -l .; exit 1; fi

clean:
	rm -rf bin/ migrations/ cmd/supervisor/migrations/
