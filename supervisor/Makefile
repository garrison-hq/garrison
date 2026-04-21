.PHONY: build test test-integration test-chaos sqlc migrate run docker lint clean

build:
	CGO_ENABLED=0 go build -o bin/supervisor ./cmd/supervisor

test:
	go test ./...

test-integration:
	go test -tags=integration ./...

test-chaos:
	go test -tags=chaos ./...

sqlc:
	sqlc generate

migrate: build
	./bin/supervisor --migrate

run: build
	./bin/supervisor

docker:
	docker build -t garrison/supervisor:dev .

lint:
	go vet ./...
	@if [ -n "$$(gofmt -l .)" ]; then gofmt -l .; exit 1; fi

clean:
	rm -rf bin/
