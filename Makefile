.PHONY: build test test-integration test-scenario lint lint-boundaries lint-security vuln toolchain-parity clean docker-build docker-up docker-down docker-reset docker-test docker-lint docker-vet

# ── Build ──

build:
	go build -o bin/spine ./service/cmd/spine

clean:
	rm -rf bin/

# ── Test ──

test:
	go test ./...

test-integration:
	go test -tags integration ./...

test-scenario:
	go test -tags scenario ./service/scenariotest/...

# ── Lint ──

lint: lint-boundaries
	golangci-lint run

# INIT-011 / TASK-005: ADR-017 dependency-direction guard.
# Refuses core/ -> service/, adapters/ -> service/, and any tier ->
# spine-management-platform. core/ -> adapters/ crossings are
# allowlisted to the documented TASK-004 follow-up surface.
lint-boundaries:
	./dev/lint-boundaries.sh

# Security-only lint: runs gosec in isolation. This is the gate for
# TASK-011 — any new security finding must be triaged and either fixed
# or suppressed with a reasoned //nolint:gosec directive before merge.
lint-security:
	golangci-lint run --enable-only=gosec ./...

# ── Supply chain (INIT-024 TASK-009) ──
# The same checks as the CI jobs `vuln` and `toolchain-parity`.
GOVULNCHECK_VERSION ?= v1.9.0

vuln:
	GOFLAGS=-mod=readonly go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

toolchain-parity:
	go run ./dev/pincheck -resolve

# ── Docker Dev ──
# Cached volumes avoid re-downloading Go modules on every run. The image is
# the Dockerfile's builder (same tag and digest; go.mod's toolchain) —
# `make toolchain-parity` fails if they drift.

DOCKER_GO = docker run --rm \
	-v "$(CURDIR)":/app \
	-v spine-gomod:/go/pkg/mod \
	-v spine-gocache:/root/.cache/go-build \
	-w /app golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af

docker-test:
	$(DOCKER_GO) go test ./...

docker-test-v:
	$(DOCKER_GO) go test ./... -v

GOLANGCI_LINT_VERSION ?= v2.11.4

docker-lint:
	$(DOCKER_GO) sh -c 'go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) && golangci-lint run ./...'

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
