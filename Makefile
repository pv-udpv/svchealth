BINARY    := svchealth
PKG       := ./cmd/svchealth
BIN_DIR   := bin
COVER_OUT := cover.out
# Packages that have tests (avoids covdata on no-test packages).
TEST_PKGS := ./internal/checks ./internal/config ./internal/store ./internal/exporter

VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build the binary into bin/
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: run
run: ## Build and run with the default config
	go run $(PKG) -config config.toml

.PHONY: test
test: ## Run all tests
	go test ./... -count=1

.PHONY: cover
cover: ## Run tests with a coverage profile + summary
	go test -coverprofile=$(COVER_OUT) $(TEST_PKGS)
	go tool cover -func=$(COVER_OUT) | tail -1

.PHONY: cover-html
cover-html: cover ## Open the HTML coverage report
	go tool cover -html=$(COVER_OUT)

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go sources
	gofmt -w ./internal ./cmd

.PHONY: fmt-check
fmt-check: ## Fail if any file is not gofmt-formatted
	@u=$$(gofmt -l ./internal ./cmd); \
	if [ -n "$$u" ]; then echo "needs gofmt:"; echo "$$u"; exit 1; fi

.PHONY: smoke
smoke: ## Run the headless smoke harness
	go run ./cmd/smoketest

.PHONY: view
view: ## Run the headless view-render harness
	go run ./cmd/viewtest

.PHONY: check
check: fmt-check vet test ## Run the full local quality gate

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build + test artifacts
	rm -rf $(BIN_DIR) $(COVER_OUT) *.db *.db-wal *.db-shm

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'
