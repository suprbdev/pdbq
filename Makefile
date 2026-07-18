GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= suprbdev/pdbq

.PHONY: help build dev test test-e2e bench lint docs docker-build docker-push compose-up compose-down example-config fuzz release

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

# Verifies, tags, and pushes; the Release workflow (release.yaml) then builds
# the binaries/image and publishes the GitHub release with generated notes.
release: ## Cut a new release (prompts for version, tags, pushes)
	@set -e; \
	git diff --quiet && git diff --cached --quiet || { echo "error: working tree dirty — commit or stash first"; exit 1; }; \
	current=$$(git describe --tags --abbrev=0 2>/dev/null || echo "(none)"); \
	echo "Current version: $$current"; \
	printf "New version (vX.Y.Z): "; read -r version; \
	echo "$$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$$' || { echo "error: invalid version '$$version' (expected vX.Y.Z)"; exit 1; }; \
	if git rev-parse -q --verify "refs/tags/$$version" >/dev/null; then echo "error: tag $$version already exists"; exit 1; fi; \
	$(MAKE) test lint; \
	git tag -a "$$version" -m "$$version"; \
	git push origin HEAD "$$version"; \
	echo "Pushed $$version — the Release workflow is publishing it:"; \
	echo "  https://github.com/suprbdev/pdbq/actions/workflows/release.yaml"

docker-build: ## Build the pdbq Docker image
	docker build -t pdbq:$(VERSION) .

# Requires `docker login` and a buildx builder (docker buildx create --use).
# VERSION must be an exact release tag — run from a tagged checkout or pass
# VERSION=vX.Y.Z explicitly.
docker-push: ## Build and push multi-arch image to Docker Hub ($(IMAGE):latest + :vX.Y.Z)
	@echo "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "error: VERSION '$(VERSION)' is not a clean vX.Y.Z tag — checkout a release tag or pass VERSION=vX.Y.Z"; exit 1; }
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):latest -t $(IMAGE):$(VERSION) \
		--push .

compose-up: ## Start the local stack detached
	docker compose up -d --build

compose-down: ## Stop the local stack and remove volumes
	docker compose down -v
