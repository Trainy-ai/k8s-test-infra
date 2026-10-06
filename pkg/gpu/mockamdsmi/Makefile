# Copyright 2026 Trainy, Inc.
# SPDX-License-Identifier: Apache-2.0
#
# AMD GPU simulator (MEP-1001). Kept apart from the top-level Makefile so the
# fork's AMD targets never conflict with upstream Mokka merges. CI
# (.github/workflows/amd-sim.yaml) runs these same targets.
#
#   make -C pkg/gpu/mockamdsmi test            go vet, gofmt and go test on the AMD paths
#   make -C pkg/gpu/mockamdsmi lint            golangci-lint with the repository config
#   make -C pkg/gpu/mockamdsmi generate        regenerate the NOT_SUPPORTED stubs
#   make -C pkg/gpu/mockamdsmi check-generated fail if the committed stubs are stale
#   make -C pkg/gpu/mockamdsmi check-abi       build both stub libraries and diff their
#                                              exports against the real ones

REPO_ROOT     := $(shell git rev-parse --show-toplevel)
PKG           := pkg/gpu/mockamdsmi
GO_DIRS       := $(PKG) cmd/generate-amdsmi-bridge
GO_PKGS       := $(addsuffix /...,$(addprefix ./,$(GO_DIRS)))
GOLANGCI_LINT ?= $(shell command -v golangci-lint || echo $(REPO_ROOT)/tmp/bin/golangci-lint)
ROCM          ?= 7.2.1
CC            ?= cc
ABI_DIR       := $(REPO_ROOT)/$(PKG)/abi/rocm-$(ROCM)
BRIDGE_DIR    := $(REPO_ROOT)/$(PKG)/bridge
BUILD_DIR     := $(REPO_ROOT)/tmp/mockamdsmi

.PHONY: vet fmt-check test lint generate check-generated stubs check-abi

vet:
	cd $(REPO_ROOT) && go vet $(GO_PKGS)

fmt-check:
	@cd $(REPO_ROOT) && out=$$(gofmt -l $(GO_DIRS)); \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

test: vet fmt-check
	cd $(REPO_ROOT) && go test -race -count=1 $(GO_PKGS)

lint:
	cd $(REPO_ROOT) && $(GOLANGCI_LINT) run $(GO_PKGS)

generate:
	cd $(REPO_ROOT) && go run ./cmd/generate-amdsmi-bridge -rocm $(ROCM)

check-generated: generate
	@cd $(REPO_ROOT) && git diff --exit-code -- $(PKG)/bridge && \
		test -z "$$(git status --porcelain -- $(PKG)/bridge)" || { \
		echo "ERROR: generated AMD SMI stubs are stale. Run 'make -C $(PKG) generate' and commit the result."; \
		exit 1; }

# Each stub library is linked under the soname consumers load it by, then its
# dynamic symbol table is diffed against the real library's. That is the ABI
# contract: amdsmi_wrapper.py binds every export when it is imported.
stubs: $(BUILD_DIR)/libamd_smi.so.26 $(BUILD_DIR)/librocm_smi64.so.1

$(BUILD_DIR)/libamd_smi.so.26: $(BRIDGE_DIR)/amdsmi/stubs_generated.c
	@mkdir -p $(BUILD_DIR)
	$(CC) -shared -fPIC -Wall -Werror -Wl,-soname,libamd_smi.so.26 -o $@ $<

$(BUILD_DIR)/librocm_smi64.so.1: $(BRIDGE_DIR)/rocmsmi/stubs_generated.c
	@mkdir -p $(BUILD_DIR)
	$(CC) -shared -fPIC -Wall -Werror -Wl,-soname,librocm_smi64.so.1 -o $@ $<

check-abi: stubs
	nm -D --defined-only $(BUILD_DIR)/libamd_smi.so.26 | awk '$$3 ~ /^amdsmi_/ {print $$3}' | sort \
		| diff -u $(ABI_DIR)/libamd_smi.so.26.exports -
	nm -D --defined-only $(BUILD_DIR)/librocm_smi64.so.1 | awk '$$3 ~ /^rsmi_/ {print $$3}' | sort \
		| diff -u $(ABI_DIR)/librocm_smi64.so.1.exports -
	@echo "ABI OK: stub exports match the real ROCm $(ROCM) libraries"
