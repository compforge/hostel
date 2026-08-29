.PHONY: help build test e2e e2e-image vet fmt lint tidy check run run-bwrap linux smoke image image-lean image-multiarch clean

BIN       := bin/hostel
ADDR      := :8872
WS_ROOT   := ./.workspace
IMAGE     := hostel:dev
VERSION   := $(shell cat VERSION)
LDFLAGS   := -X main.version=$(VERSION)
PLATFORMS := linux/amd64,linux/arm64
E2E_IMAGE ?=
TEST_FILES ?=
TEST_PACKAGES ?= ./...

# Go compiles tests at package granularity. TEST_FILES is a devloop adapter:
# turn changed _test.go paths into unique package targets instead of passing files.
ifneq ($(strip $(TEST_FILES)),)
TEST_PACKAGES := $(sort $(foreach file,$(TEST_FILES),./$(patsubst %/,%,$(dir $(file)))))
endif

TEST_TAGS :=
ifneq ($(filter ./tests/e2e,$(TEST_PACKAGES)),)
TEST_TAGS := -tags=e2e
endif

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build the hostel binary for the current platform
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/hostel

# -race is non-negotiable here: shells, bed lifecycle and cas uploads are all
# concurrent; -count=1 keeps the detector from being skipped by the test cache.
test: ## Run all tests with the race detector
	go test $(TEST_TAGS) -race -count=1 $(TEST_PACKAGES)

e2e: build ## Run the single-machine runtime contract against a real hostel binary
	HOSTEL_E2E_BINARY="$(CURDIR)/$(BIN)" go test -tags=e2e -count=1 -v ./tests/e2e

e2e-image: ## Run the full contract, including PyPI/npm/Chromium (set E2E_IMAGE)
	@test -n "$(E2E_IMAGE)" || { echo "E2E_IMAGE is required"; exit 1; }
	HOSTEL_E2E_IMAGE="$(E2E_IMAGE)" HOSTEL_E2E_USERLAND=1 \
		go test -tags=e2e -count=1 -v ./tests/e2e

vet: ## Run go vet
	go vet ./...

fmt: ## Format all Go sources
	gofmt -w .

lint: vet ## gofmt check + go vet (CI gate)
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi

tidy: ## Sync go.mod/go.sum
	go mod tidy

check: tidy lint build test ## Pre-commit gate: tidy + lint + build + race tests
	@echo "check passed"

run: build ## Run locally with no isolation (dev, any platform)
	$(BIN) --isolation dorm --workspace-root $(WS_ROOT) --addr $(ADDR)

run-bwrap: build ## Run at suite level = bwrap (Linux with bubblewrap installed)
	$(BIN) --isolation suite --workspace-root $(WS_ROOT) --addr $(ADDR)

linux: ## Cross-compile static Linux binaries (amd64 + arm64) into bin/
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/hostel-linux-amd64 ./cmd/hostel
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/hostel-linux-arm64 ./cmd/hostel

smoke: build ## Boot on a scratch port and curl the core endpoints end to end
	@set -e; \
	tmp=$$(mktemp -d); \
	$(BIN) --isolation dorm --workspace-root $$tmp/ws --addr :44799 & pid=$$!; \
	trap "kill $$pid 2>/dev/null; rm -rf $$tmp" EXIT; \
	sleep 1; \
	curl -sf localhost:44799/ping >/dev/null; \
	curl -sf localhost:44799/healthz >/dev/null; \
	curl -sfN -XPOST localhost:44799/command -H 'Content-Type: application/json' \
	  -d '{"command":"echo smoke > s.txt && cat s.txt"}' | grep -q smoke; \
	curl -sf 'localhost:44799/files/download?path=/workspace/s.txt' | grep -q smoke; \
	curl -sf -o /dev/null -w '%{http_code}' 'localhost:44799/files/download?path=/workspace/s.txt' \
	  -H 'X-Hostel-Bed: other' | grep -q 404; \
	echo "smoke OK"

image: ## Build the container image (bwrap + chromium)
	docker build -f deploy/docker/Dockerfile -t $(IMAGE) --build-arg VERSION=$(VERSION) .

image-lean: ## Build the lean image (bwrap only, no browser amenity)
	docker build -f deploy/docker/Dockerfile -t $(IMAGE)-lean --build-arg VERSION=$(VERSION) --build-arg WITH_CHROMIUM=false .

image-multiarch: ## Build + push a multi-arch image (needs buildx + a registry; set IMAGE=repo/name:tag)
	docker buildx build -f deploy/docker/Dockerfile --platform $(PLATFORMS) \
		-t $(IMAGE) --build-arg VERSION=$(VERSION) --push .

clean: ## Remove build artifacts and the dev workspace
	rm -rf bin .workspace
