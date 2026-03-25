.PHONY: build test test-integration lint clean docker-build docker-up docker-down docker-reset docker-test docker-lint docker-vet

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

# ── Docker Dev ──
# Cached volumes avoid re-downloading Go modules on every run.

DOCKER_GO = docker run --rm \
	-v "$(CURDIR)":/app \
	-v spine-gomod:/go/pkg/mod \
	-v spine-gocache:/root/.cache/go-build \
	-w /app golang:1.26-bookworm

docker-test:
	$(DOCKER_GO) go test ./...

docker-test-v:
	$(DOCKER_GO) go test ./... -v

docker-lint:
	$(DOCKER_GO) sh -c 'go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest 2>/dev/null && golangci-lint run ./...'

docker-vet:
	$(DOCKER_GO) go vet ./...

docker-cover:
	$(DOCKER_GO) go test ./... -cover

# ── Docker Compose ──

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
