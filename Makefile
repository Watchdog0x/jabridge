BINARY_NAME := jLink
GO := go
GOFLAGS := -v
LDFLAGS := -s -w
LIB_DIR := lib
INSTALL_PREFIX := /usr/local/bin
JABRA_LIB_DIR := /usr/lib/jabra

.PHONY: all build clean extract-lib lint vet fmt test install uninstall help

all: extract-lib build ## Build the project (default)

build: ## Build the binary
	$(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY_NAME) .

clean: ## Remove build artifacts
	rm -f $(BINARY_NAME)
	rm -rf $(LIB_DIR)

extract-lib: ## Extract libjabra.so from install.sh
	@mkdir -p $(LIB_DIR)
	@if [ ! -f $(LIB_DIR)/libjabra.so ]; then \
		echo "Extracting libjabra.so from install.sh..."; \
		sed -n 's/^libjabraSo="//;s/"$$//p' install.sh | xxd -r -p > $(LIB_DIR)/libjabra.so; \
		chmod 644 $(LIB_DIR)/libjabra.so; \
		echo "Done."; \
	else \
		echo "$(LIB_DIR)/libjabra.so already exists, skipping extraction."; \
	fi

lint: ## Run golangci-lint
	golangci-lint run

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Check code formatting
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Unformatted files:"; \
		echo "$$unformatted"; \
		echo "Run 'gofmt -w .' to fix."; \
		exit 1; \
	fi

test: ## Run tests
	$(GO) test -v -race ./...

install: build ## Install jLink to system
	sudo install -Dm755 $(BINARY_NAME) $(INSTALL_PREFIX)/jlink
	@echo "Installed to $(INSTALL_PREFIX)/jlink"

uninstall: ## Remove jLink from system
	sudo rm -f $(INSTALL_PREFIX)/jlink
	@echo "Removed $(INSTALL_PREFIX)/jlink"

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
