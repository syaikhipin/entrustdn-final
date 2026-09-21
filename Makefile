# Thresh monorepo — one command to run the tracer bullet.
# make dev boots Postgres (docker), backend, agent, and web together.

SHELL := /bin/bash

DATABASE_URL ?= postgres://thresh:thresh@localhost:5432/thresh?sslmode=disable
AGENT_BASE_URL ?= http://localhost:8001
BACKEND_ADDR ?= :8080
NUXT_PUBLIC_BACKEND_BASE_URL ?= http://localhost:8080

.PHONY: help dev dev-postgres dev-backend dev-agent dev-web test test-backend test-agent test-web build clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

dev: ## Boot everything: postgres + backend + agent + web (installs deps first)
	@echo "==> Preparing dependencies…"
	@docker compose up -d --wait postgres
	@(cd agent && uv sync --quiet)
	@(cd web && [ -d node_modules ] || pnpm install --silent)
	@echo "==> Starting agent sidecar on :8001…"
	@(cd agent && uv run python -m thresh_agent > /tmp/thresh-agent.log 2>&1 &) ; sleep 2
	@echo "==> Starting backend on :8080…"
	@(cd backend && DATABASE_URL=$(DATABASE_URL) AGENT_BASE_URL=$(AGENT_BASE_URL) BACKEND_ADDR=$(BACKEND_ADDR) go run ./cmd/api > /tmp/thresh-backend.log 2>&1 &) ; sleep 2
	@echo "==> Starting web on :3000…"
	@(cd web && NUXT_PUBLIC_BACKEND_BASE_URL=$(NUXT_PUBLIC_BACKEND_BASE_URL) ./node_modules/.bin/nuxt dev > /tmp/thresh-web.log 2>&1 &)
	@sleep 3
	@echo ""
	@echo "Thresh is up:"
	@echo "  web      → http://localhost:3000"
	@echo "  backend  → http://localhost:8080/api/v1/status"
	@echo "  agent    → http://localhost:8001/health"
	@echo "logs: /tmp/thresh-{agent,backend,web}.log (make stop kills the services)"

.PHONY: stop
stop: ## Stop agent, backend, web; tear down Postgres
	-@pkill -f thresh_agent 2>/dev/null
	-@pkill -f "go run ./cmd/api" 2>/dev/null
	-@pkill -f "cmd/api" 2>/dev/null
	-@pkill -f "nuxt dev" 2>/dev/null
	-@docker compose down
	@echo "stopped."

test: test-backend test-agent test-web ## Run all three suites

test-backend: ## go test -race ./... (postgres tests need TEST_DATABASE_URL)
	cd backend && go test -race ./...

test-backend-pg: ## go test -race with Postgres up (compose)
	docker compose up -d --wait postgres
	cd backend && TEST_DATABASE_URL=$(DATABASE_URL) go test -race ./...

test-agent: ## pytest via uv
	cd agent && uv run pytest tests/ -q

test-web: ## vitest via pnpm
	cd web && ./node_modules/.bin/vitest run

build: ## Build all three
	cd backend && go build ./...
	cd web && ./node_modules/.bin/nuxt build

clean: ## Remove build artifacts
	rm -rf web/.output web/.nuxt
