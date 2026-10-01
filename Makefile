# Crash-safe object store — developer entry points (CLAUDE.md §5).
# Indented recipe lines must be TABS; this file is generated with tabs.

COMPOSE := docker compose -f deploy/docker-compose.yml
COMPOSE_PROD := $(COMPOSE) -f deploy/docker-compose.prod.yml

.PHONY: help up down migrate sqlc test lint build crash-test e2e compose-config

help: ## Show available targets
	@grep -hE '^[a-z0-9-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-14s %s\n", $$1, $$2}'

up: ## Start the full Compose stack
	$(COMPOSE) up -d --build

down: ## Stop the stack
	$(COMPOSE) down

migrate: ## Apply database migrations (forward-only, §14)
	migrate -path db/migrations -database "$$DATABASE_URL" up

sqlc: ## Regenerate typed queries from db/queries
	sqlc generate

build: ## Compile every binary into ./bin
	go build -o bin/ ./cmd/...

test: ## go vet + full test suite
	go vet ./...
	go test ./... -race -count=1

lint: ## Static analysis
	golangci-lint run ./...

crash-test: ## WAL crash-injection harness (§15)
	go test ./internal/wal/... -run Crash -count=1

e2e: ## Compose-based failure scenarios: kill node, heal, replay (§15)
	go test ./test/e2e/... -tags=e2e -count=1

compose-config: ## Assert the prod compose overlay still merges cleanly (§17 parity)
	$(COMPOSE_PROD) config > /dev/null && echo "compose config OK"
