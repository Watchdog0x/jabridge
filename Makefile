BINARY_NAME := jabridge
UPDATER_NAME := jafw
VERSION ?= 1.0.0
GO ?= go
BUILD_DIR ?= dist/bin
PREFIX ?= /usr/local
MODULE_PATH := github.com/Watchdog0x/jabridge
LDFLAGS := -s -w -X $(MODULE_PATH)/internal/buildinfo.Version=$(VERSION)

.PHONY: all build check clean completion-check fmt install test test-static uninstall vet

all: check build

build:
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BUILD_DIR)/$(UPDATER_NAME) ./cmd/jafw

fmt:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		printf 'Unformatted files:\n%s\n' "$$unformatted"; \
		exit 1; \
	fi

vet:
	CGO_ENABLED=0 $(GO) vet ./...

test:
	$(GO) test -race ./...

test-static:
	CGO_ENABLED=0 $(GO) test ./...

completion-check:
	bash -n internal/completion/jabridge.bash internal/completion/jafw.bash

check: fmt vet test test-static completion-check

install: build
	install -Dm755 $(BUILD_DIR)/$(BINARY_NAME) $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME)
	install -Dm755 $(BUILD_DIR)/$(UPDATER_NAME) $(DESTDIR)$(PREFIX)/bin/$(UPDATER_NAME)
	install -Dm644 internal/completion/jabridge.bash $(DESTDIR)$(PREFIX)/share/bash-completion/completions/jabridge
	install -Dm644 internal/completion/jafw.bash $(DESTDIR)$(PREFIX)/share/bash-completion/completions/jafw

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY_NAME) $(DESTDIR)$(PREFIX)/bin/$(UPDATER_NAME)
	rm -f $(DESTDIR)$(PREFIX)/share/bash-completion/completions/jabridge
	rm -f $(DESTDIR)$(PREFIX)/share/bash-completion/completions/jafw

clean:
	rm -f $(BUILD_DIR)/$(BINARY_NAME) $(BUILD_DIR)/$(UPDATER_NAME)
