# shep — Herdr-first project launcher
#
# Variables passed through the environment override defaults.
BINARY  ?= shep
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
PKG     := ./...
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)

GO      ?= go
GOFLAGS ?=

.PHONY: all build test vet lint tidy clean install help

all: build

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/shep

test:
	$(GO) test -race $(GOFLAGS) $(PKG)

vet:
	$(GO) vet $(PKG)

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed; skipping"; exit 0; }
	golangci-lint run

tidy:
	$(GO) mod tidy

clean:
	rm -f $(BINARY) coverage.txt

install: build
	$(GO) install $(GOFLAGS) -ldflags "$(LDFLAGS)" ./cmd/shep

help:
	@echo "shep Makefile targets:"
	@echo "  build    - compile the shep binary (./$(BINARY))"
	@echo "  test     - run tests with -race"
	@echo "  vet      - run go vet"
	@echo "  lint     - run golangci-lint (if installed)"
	@echo "  tidy     - go mod tidy"
	@echo "  clean    - remove build artifacts"
	@echo "  install  - go install the shep binary"