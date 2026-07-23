# Copyright (C) 2026 Henry Stern
# SPDX-License-Identifier: MIT

# go-activesync — top-level developer commands.
#
# `make` (no target) prints the help. The two everyday targets are:
#   make ci          run everything CI runs (gofmt + vet + tidy + vuln + race tests)
#   make svg         re-render any .dot whose .svg is out of date

# All .dot sources discovered automatically, anywhere in the tree.
DOT_FILES := $(shell find . -type f -name '*.dot' -not -path './.git/*')
SVG_FILES := $(DOT_FILES:.dot=.svg)

# Tools installed on demand into $GOPATH/bin (matches the CI behaviour).
GOBIN ?= $(shell go env GOPATH)/bin
GOVULNCHECK := $(GOBIN)/govulncheck

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Discoverable help (anything with `## ` after the target name shows up).
# ---------------------------------------------------------------------------

.PHONY: help
help:
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST) | sort

# ---------------------------------------------------------------------------
# CI parity — run the same checks the GitHub workflow runs.
# ---------------------------------------------------------------------------

.PHONY: ci
ci: lint test ## Run everything CI runs (lint + race tests + coverage)

.PHONY: lint
lint: gofmt-check vet tidy-check vulncheck ## Static checks (no side effects)

.PHONY: gofmt-check
gofmt-check: ## Fail if any file would be modified by gofmt
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "gofmt drift in:"; echo "$$out"; \
		echo "run: make fmt"; \
		exit 1; \
	fi

.PHONY: fmt
fmt: ## gofmt -w on every Go file
	gofmt -w .

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: tidy-check
tidy-check: ## Fail if `go mod tidy` would change go.mod/go.sum
	@cp go.mod /tmp/go.mod.bak; cp go.sum /tmp/go.sum.bak; \
	go mod tidy; \
	if ! diff -q /tmp/go.mod.bak go.mod >/dev/null || ! diff -q /tmp/go.sum.bak go.sum >/dev/null; then \
		mv /tmp/go.mod.bak go.mod; mv /tmp/go.sum.bak go.sum; \
		echo "go.mod/go.sum out of sync; run: go mod tidy"; \
		exit 1; \
	fi; \
	rm -f /tmp/go.mod.bak /tmp/go.sum.bak

.PHONY: vulncheck
vulncheck: $(GOVULNCHECK) ## govulncheck against the standard module
	$(GOVULNCHECK) ./...

$(GOVULNCHECK):
	go install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: build
build: ## Compile every package
	go build ./...

.PHONY: test
test: ## Race detector + coverage (matches CI)
	go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -5

.PHONY: cover
cover: test ## Open coverage HTML in your browser
	go tool cover -html=coverage.out

# ---------------------------------------------------------------------------
# Diagrams — pattern rule turns each .dot into a .svg, only when stale.
# ---------------------------------------------------------------------------

.PHONY: svg
svg: $(SVG_FILES) ## Render every .dot to its sibling .svg

%.svg: %.dot
	@command -v dot >/dev/null || { echo "graphviz 'dot' not installed (brew install graphviz)"; exit 1; }
	dot -Tsvg $< -o $@
	@echo "rendered $@"

# ---------------------------------------------------------------------------
# Integration tests — re-enter testenv/Makefile for the live Z-Push run.
# ---------------------------------------------------------------------------

.PHONY: integration
integration: ## Bring up testenv, run integration suite, tear down
	$(MAKE) -C testenv up
	$(MAKE) -C testenv test
	$(MAKE) -C testenv down

# ---------------------------------------------------------------------------
# Housekeeping.
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove generated coverage artefacts (SVGs are checked in, not removed)
	rm -f coverage.out coverage.html

.PHONY: all
all: svg ci ## Re-render diagrams + run the full CI parity suite
