# Crash-safe object store — developer entry points.
# Indented recipe lines must be TABS; this file is generated with tabs.

SQLC_VERSION := 1.27.0

COMPOSE := docker compose --project-directory . -f deploy/docker-compose.yml
COMPOSE_PROD := $(COMPOSE) -f deploy/docker-compose.prod.yml

.PHONY: help up down migrate sqlc test lint build crash-test e2e compose-config

help: ## Show available targets
	@grep -hE '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

up: ## Start the full Compose stack
	$(COMPOSE) up -d --build

down: ## Stop the stack
	$(COMPOSE) down

migrate: ## Apply database migrations (forward-only)
	migrate -path db/migrations -database "$$DATABASE_URL" up

sqlc: ## Regenerate typed queries from db/queries
	@if command -v sqlc >/dev/null 2>&1; then \
		sqlc generate; \
	else \
		echo "sqlc not on PATH, falling back to docker"; \
		docker run --rm -v "$(CURDIR):/src" -w /src sqlc/sqlc:$(SQLC_VERSION) generate; \
	fi

build: ## Compile every binary into ./bin
	go build -o bin/ ./cmd/...

test: ## go vet + full test suite
	go vet ./...
	go test ./... -race -count=1

lint: ## Static analysis
	golangci-lint run ./...

crash-test: ## WAL crash-injection harness
	go test ./internal/wal/... -run Crash -count=1

e2e: ## Compose-based failure scenarios: kill node, heal, replay
	go test ./test/e2e/... -tags=e2e -count=1

compose-config: ## Assert the prod compose overlay still merges cleanly
	$(COMPOSE_PROD) config > /dev/null && echo "compose config OK"
