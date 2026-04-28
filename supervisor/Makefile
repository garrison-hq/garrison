.PHONY: build test test-integration test-chaos sqlc migrate run docker lint vet-vaultlog clean copy-migrations seed-engineer-agent

GO ?= $(shell which go 2>/dev/null || echo /home/jeroennouws/.local/go/bin/go)

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
	go test -coverprofile=coverage.out -covermode=atomic ./...

test-integration: copy-migrations
	go test -tags=integration -coverprofile=int-coverage.out -covermode=atomic ./...

test-chaos: copy-migrations
	go test -tags=chaos ./...

sqlc:
	sqlc generate

# seed-engineer-agent re-embeds migrations/seed/engineer.md into the
# engineer-role seed UPDATE in the M2.2.1 migration. Idempotent — run
# it whenever the markdown file changes so the DB seed stays in sync.
# The paths are relative to supervisor/ (this Makefile's dir).
# (M2.1 embedded into examples/agents/engineer.md; M2.2 moved the
# canonical location to migrations/seed/; M2.2.1 points the embed at
# the newest migration so shipped migrations stay frozen.)
seed-engineer-agent:
	go run ./internal/tools/embed-agent-md \
		-role=engineer \
		../migrations/seed/engineer.md \
		../migrations/20260424000006_m2_2_2_compliance_calibration.sql

# seed-qa-engineer-agent re-embeds migrations/seed/qa-engineer.md into
# the qa-engineer UPDATE in the M2.2.1 migration. Same tool, different
# role.
seed-qa-engineer-agent:
	go run ./internal/tools/embed-agent-md \
		-role=qa-engineer \
		../migrations/seed/qa-engineer.md \
		../migrations/20260424000006_m2_2_2_compliance_calibration.sql

migrate: build
	./bin/supervisor --migrate

run: build
	./bin/supervisor

docker: copy-migrations
	docker build -t garrison/supervisor:dev .

# copy-migrations is required because cmd/supervisor/migrate.go has a
# //go:embed directive that go vet and the compiler resolve at type-check
# time — without the staged files, `go vet ./...` fails.
lint: copy-migrations vet-vaultlog
	go vet ./...
	@if [ -n "$$(gofmt -l .)" ]; then gofmt -l .; exit 1; fi

# vet-vaultlog runs the custom AST analyzer that rejects slog/fmt calls
# passing vault.SecretValue arguments (Rule 6 / SC-410).
vet-vaultlog: copy-migrations
	$(GO) run ./tools/vaultlog/cmd/vaultlog ./...

clean:
	rm -rf bin/ migrations/ cmd/supervisor/migrations/

# M5.1 — chat container image. Built out-of-band; the supervisor's
# docker-compose.yml does NOT build either image — operators (or CI)
# invoke these targets before `compose up -d`.
chat-image:
	docker build -t garrison-claude:m5 -f Dockerfile.claude .

mockclaude-chat-image:
	docker build -t garrison-mockclaude:m5 -f Dockerfile.mockclaude.chat .

.PHONY: chat-image mockclaude-chat-image
