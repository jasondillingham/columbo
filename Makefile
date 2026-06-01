.PHONY: build install test check vet tidy clean help

# Same pattern as Leonard/bosun: VERSION comes from git describe (or a
# pinned value when building from a non-git source), injected via
# ldflags. Single source of truth lives in internal/version/version.go.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-pre)
LDFLAGS := -X github.com/jasondillingham/columbo/internal/version.Version=$(VERSION)

build: ## Build columbo + columbo-mcp into the repo root
	go build -ldflags="$(LDFLAGS)" -o columbo ./cmd/columbo
	go build -ldflags="$(LDFLAGS)" -o columbo-mcp ./cmd/columbo-mcp

install: ## go install both binaries to $$GOPATH/bin
	go install -ldflags="$(LDFLAGS)" ./cmd/columbo ./cmd/columbo-mcp

test: ## Run unit tests
	go test ./...

vet: ## go vet ./...
	go vet ./...

check: vet test ## Lint + test

tidy: ## go mod tidy
	go mod tidy

clean: ## Remove built binaries
	rm -f columbo columbo-mcp

help:
	@echo "Columbo Makefile targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'
