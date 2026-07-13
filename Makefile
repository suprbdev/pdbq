GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help build dev test test-e2e bench lint docs docker-build compose-up compose-down example-config fuzz

.DEFAULT_GOAL := help

help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the pdbq binary into bin/
	$(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/pdbq ./cmd/pdbq

dev: ## Full local stack (Postgres + pdbq in watch mode)
	docker compose up --build

test: ## Unit + golden tests (hermetic, no database needed)
	$(GO) test -race ./...

# -p isolates the test stack from the dev stack: both compose files live in
# the repo root, so the default (directory-derived) project name would collide
# and `down -v` here would tear down the dev database.
test-e2e: ## End-to-end suite against a disposable Postgres
	docker compose -p pdbq-test -f compose.test.yaml up -d --wait
	PDBQ_TEST_DATABASE_URL="postgres://pdbq:pdbq@localhost:5433/pdbq_test" $(GO) test -race -count=1 ./test/; \
	status=$$?; docker compose -p pdbq-test -f compose.test.yaml down -v; exit $$status

bench: ## Run compile benchmarks
	$(GO) test -bench=. -benchmem -run=^$$ ./internal/compile/

fuzz: ## Fuzz the filter compiler for 30s
	$(GO) test -fuzz=FuzzFilter -fuzztime=30s ./internal/compile/

lint: ## Run golangci-lint (falls back to go vet)
	golangci-lint run ./... || $(GO) vet ./...

example-config: build ## Regenerate the committed reference YAML from the structs
	./bin/pdbq config example > examples/pdbq.example.yaml

docker-build: ## Build the pdbq Docker image
	docker build -t pdbq:$(VERSION) .

compose-up: ## Start the local stack detached
	docker compose up -d --build

compose-down: ## Stop the local stack and remove volumes
	docker compose down -v
