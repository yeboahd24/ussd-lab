BINARY  := ussd
PKG     := ./cmd/ussd

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: build test lint run clean tidy

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

test:
	go test ./... -race -count=1

lint:
	go vet ./...
	gofmt -l .

run: build
	./$(BINARY)

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) coverage.out
	rm -rf dist/
