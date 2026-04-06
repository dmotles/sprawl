.PHONY: validate build fmt-check lint test clean install fmt hooks

# Default target — full quality gauntlet
validate: build fmt-check lint test

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

build:
	go build -ldflags "$(LDFLAGS)" -o sprawl .

fmt:
	golangci-lint fmt ./...

fmt-check:
	@echo "Checking formatting..."
	@test -z "$$(golangci-lint fmt --diff ./...)" || (echo "Files need formatting. Run 'make fmt' to fix." && exit 1)

lint:
	golangci-lint run ./...

test:
	go test ./...

GOBIN ?= $(HOME)/.local/bin

install:
	GOBIN=$(GOBIN) go install .

clean:
	rm -f sprawl

hooks:
	ln -sf ../../scripts/pre-commit .git/hooks/pre-commit
	chmod +x .git/hooks/pre-commit
	@echo "Pre-commit hook installed."
