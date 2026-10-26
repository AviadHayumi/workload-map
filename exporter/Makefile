# SPDX-License-Identifier: Apache-2.0
# Copyright (c) 2026 NVIDIA Corporation

export GOWORK := off

# Location to install dependencies to, and to output built binaries.
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# Version is derived automatically from git. Falls back to "0.0.0-main" when
# there are no tags or git is unavailable (e.g. a bare source tarball).
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-main")

# Container image settings (defaults for ghcr.io/dsx-ai-factory/workload-map OSS publishing).
IMAGE_REGISTRY ?= ghcr.io/dsx-ai-factory/workload-map
IMAGE_NAME      ?= karta-exporter
IMAGE_TAG       ?= $(VERSION)
IMAGE           ?= $(IMAGE_REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Architectures for `make docker-buildx-push`
PLATFORMS ?= linux/amd64 linux/arm64
empty :=
space := $(empty) $(empty)
comma := ,
PLATFORMS_CSV = $(subst $(space),$(comma),$(PLATFORMS))

CONTAINER_TOOL ?= docker
BUILD_ARGS     ?=

VERSION_PKG = github.com/dsx-ai-factory/workload-map/karta/sdk/pkg/version
LDFLAGS     = -ldflags "-X $(VERSION_PKG).version=$(VERSION)"

# Reuse the root Makefile's pinned tools and versioned binary paths.
GOLANGCI_LINT_VERSION ?= $(shell awk '$$1 == "GOLANGCI_LINT_VERSION" {print $$3; exit}' ../Makefile)
GOLANGCI_LINT ?= $(abspath ../bin/golangci-lint-$(GOLANGCI_LINT_VERSION))
GO_VERSION ?= $(shell awk '/^go / {print $$2; exit}' go.mod)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt (fixes files in place)
	go fmt ./...

.PHONY: fmt-check
fmt-check: ## Check formatting without modifying files (intended for CI)
	@set -e; \
	dirs="$$(go list -f '{{.Dir}}' ./...)"; \
	unformatted="$$(gofmt -l $$dirs)"; \
	test -z "$$unformatted" || (echo "go fmt required:"; echo "$$unformatted"; exit 1)

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: golangci-lint ## Run golangci-lint (set VERBOSE=1 for -v)
	$(GOLANGCI_LINT) run $(if $(VERBOSE),-v) -c ../.golangci.yml

.PHONY: test
test: ## Run unit tests
	go test -race -coverprofile=cover.out ./...

.PHONY: check
check: fmt-check vet lint test ## Run all checks (fmt-check, vet, lint, test)

# This module can select dependency versions different from the library and CLI.
GO_LICENCE_DETECTOR_VERSION ?= $(shell awk '$$1 == "GO_LICENCE_DETECTOR_VERSION" {print $$3; exit}' ../Makefile)
GO_LICENCE_DETECTOR ?= $(abspath ../bin/go-licence-detector-$(GO_LICENCE_DETECTOR_VERSION))

.PHONY: generate-licenses
generate-licenses: $(LOCALBIN) ## Generate the exporter's dependency license reports.
	@test -x "$(GO_LICENCE_DETECTOR)" || $(MAKE) -C .. go-licence-detector \
		GO_LICENCE_DETECTOR="$(GO_LICENCE_DETECTOR)" GO_LICENCE_DETECTOR_VERSION="$(GO_LICENCE_DETECTOR_VERSION)"
	go mod download -json > $(LOCALBIN)/deps.json
	$(GO_LICENCE_DETECTOR) -in $(LOCALBIN)/deps.json \
		-noticeTemplate=../hack/licenses/notice.tpl -noticeOut=NOTICE \
		-depsTemplate=../hack/licenses/third_party_licenses.tpl -depsOut=THIRD_PARTY_LICENSES

##@ Build

.PHONY: build
build: ## Build the karta-exporter binary for the host OS/arch
	mkdir -p $(LOCALBIN)
	go build -trimpath $(LDFLAGS) -o $(LOCALBIN)/karta-exporter ./cmd

##@ Docker

.PHONY: docker-build
docker-build: ## Build Docker image for the host arch
	$(CONTAINER_TOOL) build $(BUILD_ARGS) \
		--build-arg TARGETARCH=$(shell go env GOARCH) \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg VERSION=$(VERSION) \
		--tag $(IMAGE) \
		-f Dockerfile \
		..

.PHONY: docker-buildx-push
docker-buildx-push: ## Build and push a multi-arch Docker image via BuildKit (requires Docker)
	@[ "$(CONTAINER_TOOL)" = "docker" ] || (echo "Error: docker-buildx-push requires CONTAINER_TOOL=docker (got '$(CONTAINER_TOOL)')" >&2; exit 1)
	$(CONTAINER_TOOL) buildx build $(BUILD_ARGS) \
		--platform $(PLATFORMS_CSV) \
		--build-arg GO_VERSION=$(GO_VERSION) \
		--build-arg VERSION=$(VERSION) \
		--tag $(IMAGE) \
		-f Dockerfile \
		--push \
		..

.PHONY: docker-push
docker-push: ## Push the Docker image
	$(CONTAINER_TOOL) push $(IMAGE)

##@ Tools

.PHONY: golangci-lint
golangci-lint: ## Download the root Makefile's pinned golangci-lint if necessary.
	$(MAKE) -C .. golangci-lint \
		GOLANGCI_LINT="$(GOLANGCI_LINT)" GOLANGCI_LINT_VERSION="$(GOLANGCI_LINT_VERSION)"

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
	/^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-25s\033[0m %s\n", $$1, $$2 } \
	/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help
