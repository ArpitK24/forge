# Forge developer tasks. Run `make help` for the list.

BINARY   := forge
PKG      := github.com/ArpitK24/forge
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(PKG)/internal/core.AppVersion=$(VERSION) \
	-X $(PKG)/internal/core.AppCommit=$(COMMIT) \
	-X $(PKG)/internal/core.AppBuildDate=$(DATE)

.DEFAULT_GOAL := help
.PHONY: help build test race cover fmt fmt-check vet lint vuln check docker clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build ./dist/forge for this machine
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/forge

test: ## Run unit tests
	go test -count=1 ./...

race: ## Run tests with the race detector (needs cgo and a C compiler)
	CGO_ENABLED=1 go test -race -count=1 ./...

cover: ## Write coverage.out and print the total
	go test -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

fmt: ## Format all Go files
	gofmt -w .

fmt-check: ## Fail if any file needs gofmt
	@test -z "$$(gofmt -l .)" || (echo "Run 'make fmt':"; gofmt -l .; exit 1)

vet: ## go vet
	go vet ./...

lint: ## golangci-lint (https://golangci-lint.run)
	golangci-lint run ./...

vuln: ## Scan dependencies for known vulnerabilities
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt-check vet lint race ## Everything CI checks

docker: ## Build the Docker image
	docker build -t $(BINARY):$(VERSION) .

clean: ## Remove build output
	rm -rf dist coverage.out
