# Each service is its own Go module, so every Go and ko command runs from inside the
# service directory. vote, worker and result live in microservices/; monolith is the
# single-image variant (vote + result on one Postgres-only server).
#
# Every target takes an optional scope: `make dev microservices`, `make dev monolith`.
# Without one, the target applies to both.

SCOPES := microservices monolith
SELECTED := $(or $(filter $(SCOPES),$(MAKECMDGOALS)),$(SCOPES))
WITH_MICROSERVICES := $(filter microservices,$(SELECTED))
WITH_MONOLITH := $(filter monolith,$(SELECTED))

SERVICES := $(if $(WITH_MICROSERVICES),microservices/vote microservices/worker microservices/result) $(if $(WITH_MONOLITH),monolith)
# Compose services of the scope (db is shared and has no profile).
COMPOSE_SERVICES := $(if $(WITH_MICROSERVICES),valkey vote worker result) $(if $(WITH_MONOLITH),monolith)
PROFILES := $(foreach s,$(SELECTED),--profile $(s))

GO ?= go
COMPOSE ?= docker compose
BIN := $(CURDIR)/bin

# Images are named $(REGISTRY)/<service>:$(TAG), the same naming as the k8s manifests.
REGISTRY ?= rgy.k8s.devops-svc-ag.com/polytech
TAG ?= dev
# Platforms pushed by `make push` (comma-separated). `make build` only targets the host.
PLATFORMS ?= linux/amd64,linux/arm64
LOCAL_PLATFORM ?= linux/$(shell $(GO) env GOARCH)
# -race needs cgo (a C compiler); use `make test TEST_FLAGS=` without one.
TEST_FLAGS ?= -race -shuffle=on

# Tools run through `go run` at pinned versions, so nothing needs installing.
# Override with a local binary if you have one, e.g. `make lint GOLANGCI_LINT=golangci-lint`.
KO ?= $(GO) run github.com/google/ko@v0.19.1
GOLANGCI_LINT ?= $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK ?= $(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0

KO_FLAGS := --base-import-paths --tags='$(TAG)' --sbom=none

# ko only reads DOCKER_HOST and ignores `docker context`, so `make build` hands it the
# active context's endpoint (rootless Docker, Docker Desktop...) unless DOCKER_HOST is set.
DOCKER_ENDPOINT = $(or $(DOCKER_HOST),$(shell docker context inspect --format '{{.Endpoints.docker.Host}}'))

# Runs a command in each service directory and stops at the first failure.
define each_service
	@set -e; for s in $(SERVICES); do echo "==> $$s"; (cd $$s && $(1)); done
endef

.DEFAULT_GOAL := help

.PHONY: $(SCOPES) help build-go build push dev up down logs test lint lint-fix fmt audit tidy tidy-check check clean

# Scopes are only read through MAKECMDGOALS, they do nothing by themselves.
$(SCOPES):
	@:

## help: Show this help message
help:
	@echo "Usage: make [target] [microservices|monolith] [REGISTRY=...] [TAG=...]"; echo "Without a scope, the target applies to both."
	@awk 'sub(/^## /, "") { split($$0, a, ": "); printf "  %-10s %s\n", a[1], substr($$0, length(a[1]) + 3) }' $(MAKEFILE_LIST)

## build-go: Build the Go binaries into ./bin
build-go:
	$(call each_service,$(GO) build -o $(BIN)/$$(basename $$s) .)

## build: Build the container images with ko and load them into the local Docker daemon
build:
	$(call each_service,DOCKER_HOST='$(DOCKER_ENDPOINT)' KO_DOCKER_REPO='$(REGISTRY)' $(KO) build --local $(KO_FLAGS) --platform='$(LOCAL_PLATFORM)' .)

## push: Build multi-arch images with ko and push them to REGISTRY (TAG is required)
push:
	@test '$(TAG)' != dev || { echo "push: set TAG, e.g. make push TAG=1.0.0"; exit 1; }
	$(call each_service,KO_DOCKER_REPO='$(REGISTRY)' $(KO) build $(KO_FLAGS) --platform='$(PLATFORMS)' .)

## dev: Build the images, start the stack, print its URLs and follow the logs
dev: build up
	@echo "Following logs (Ctrl+C stops following, the stack keeps running; 'make down' stops it)"
	@$(COMPOSE) $(PROFILES) logs -f --tail=0 $(COMPOSE_SERVICES) || true

## up: Start the full stack in the background with the existing images
up:
	REGISTRY='$(REGISTRY)' TAG='$(TAG)' $(COMPOSE) $(PROFILES) up -d --wait
	@echo
	@$(if $(WITH_MICROSERVICES),echo "  Microservices  vote: http://localhost:5000  result: http://localhost:5001";)
	@$(if $(WITH_MONOLITH),echo "  Monolith       vote: http://localhost:5002/vote/  result: http://localhost:5002/result/";)
	@echo

## down: Stop the stack (keeps the database volume)
down:
	$(if $(and $(WITH_MICROSERVICES),$(WITH_MONOLITH)),$(COMPOSE) $(PROFILES) down,$(COMPOSE) $(PROFILES) rm -sf $(COMPOSE_SERVICES))

## logs: Follow the logs of the stack
logs:
	$(COMPOSE) $(PROFILES) logs -f $(COMPOSE_SERVICES)

## test: Run the tests (race detector and shuffled order by default)
test:
	$(call each_service,$(GO) test $(TEST_FLAGS) ./...)

## lint: Run golangci-lint
lint:
	$(call each_service,$(GOLANGCI_LINT) run --config $(CURDIR)/.golangci.yml ./...)

## lint-fix: Run golangci-lint and auto-fix what it can
lint-fix:
	$(call each_service,$(GOLANGCI_LINT) run --fix --config $(CURDIR)/.golangci.yml ./...)

## fmt: Format the code
fmt:
	$(call each_service,$(GOLANGCI_LINT) fmt --config $(CURDIR)/.golangci.yml ./...)

## audit: Scan dependencies for known vulnerabilities
audit:
	$(call each_service,$(GOVULNCHECK) ./...)

## tidy: Tidy the go.mod and go.sum files
tidy:
	$(call each_service,$(GO) mod tidy)

## tidy-check: Fail if go.mod/go.sum are not tidy, and verify module checksums
tidy-check:
	$(call each_service,$(GO) mod tidy -diff && $(GO) mod verify)

## check: Run every check CI runs (lint, test, audit, tidy-check)
check: lint test audit tidy-check

## clean: Remove binaries, then stop the stack and delete its volumes
clean:
	rm -rf $(BIN)
	-$(if $(and $(WITH_MICROSERVICES),$(WITH_MONOLITH)),$(COMPOSE) $(PROFILES) down -v,$(COMPOSE) $(PROFILES) rm -sfv $(COMPOSE_SERVICES))
