GO ?= go
BINDIR ?= $(HOME)/.local/bin
BUILD_DIR ?= build

.PHONY: build install test vet fmt check

build:
	@mkdir -p "$(BUILD_DIR)"
	$(GO) build -o "$(BUILD_DIR)/listen" ./cmd/listen

install:
	@mkdir -p "$(BINDIR)"
	GOBIN="$(BINDIR)" $(GO) install ./cmd/listen

fmt:
	gofmt -w ./cmd ./internal

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

check: fmt test vet
