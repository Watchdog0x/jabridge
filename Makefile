BINARY_NAME := jabridge
VERSION ?= 1.0.0
GO ?= go
GOLANGCI_LINT ?= golangci-lint
BUILD_DIR ?= dist/bin
PREFIX ?= /usr/local
MODULE_PATH := github.com/Watchdog0x/jabridge
LDFLAGS := -s -w -X $(MODULE_PATH)/internal/buildinfo.Version=$(VERSION)

.PHONY: all build check clean completion-check fmt install lint test test-static uninstall vet

all: check build

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .

fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		printf 'Unformatted files:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

vet:
	CGO_ENABLED=0 $(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run --timeout 5m ./...

test:
	$(GO) test -race ./...

test-static:
	CGO_ENABLED=0 $(GO) test ./...

completion-check:
	bash -n internal/completion/jabridge.bash

check: fmt vet test test-static completion-check

install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY_NAME) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	install -Dm644 internal/completion/jabridge.bash $(DESTDIR)$(PREFIX)/share/bash-completion/completions/jabridge

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	rm -f $(DESTDIR)$(PREFIX)/share/bash-completion/completions/jabridge

clean:
	rm -f $(BUILD_DIR)/$(BINARY_NAME)
