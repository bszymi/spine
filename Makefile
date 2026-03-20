.PHONY: build test test-integration lint clean docker-build docker-up docker-down docker-reset

# ── Build ──

build:
	go build -o bin/spine ./cmd/spine

clean:
	rm -rf bin/

# ── Test ──

test:
	go test ./...

test-integration:
	go test -tags integration ./...

# ── Lint ──

lint:
	golangci-lint run

# ── Docker ──

docker-build:
	docker compose build

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-reset:
	docker compose down -v
	docker compose --profile setup up -d
	@echo "Waiting for setup services..."
	@sleep 5
	docker compose up -d spine

# ── Database ──

migrate:
	docker compose run --rm spine-migrate

# ── Test Infrastructure ──

test-db-up:
	docker compose -f docker-compose.test.yaml up -d

test-db-down:
	docker compose -f docker-compose.test.yaml down
