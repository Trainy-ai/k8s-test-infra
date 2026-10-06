# Copyright 2026 Trainy, Inc.
# SPDX-License-Identifier: Apache-2.0
#
# AMD GPU simulator (MEP-1001). Kept apart from the top-level Makefile so the
# fork's AMD targets never conflict with upstream Mokka merges. CI
# (.github/workflows/amd-sim.yaml) runs these same targets.
#
#   make -C pkg/gpu/mockamdsmi test   go vet, gofmt and go test on the AMD paths
#   make -C pkg/gpu/mockamdsmi lint   golangci-lint with the repository config

REPO_ROOT     := $(shell git rev-parse --show-toplevel)
PKG           := pkg/gpu/mockamdsmi
GO_DIRS       := $(PKG)
GO_PKGS       := $(addsuffix /...,$(addprefix ./,$(GO_DIRS)))
GOLANGCI_LINT ?= $(shell command -v golangci-lint || echo $(REPO_ROOT)/tmp/bin/golangci-lint)

.PHONY: vet fmt-check test lint

vet:
	cd $(REPO_ROOT) && go vet $(GO_PKGS)

fmt-check:
	@cd $(REPO_ROOT) && out=$$(gofmt -l $(GO_DIRS)); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

test: vet fmt-check
	cd $(REPO_ROOT) && go test -race -count=1 $(GO_PKGS)

lint:
	cd $(REPO_ROOT) && $(GOLANGCI_LINT) run $(GO_PKGS)
