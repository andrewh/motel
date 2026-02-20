VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: build test lint clean

build:
	go build -ldflags "$(LDFLAGS)" -o build/motel ./cmd/motel

test:
	go test ./...

lint:
	@test -z "$$(gofmt -s -l .)" || (gofmt -s -l . && exit 1)
	go vet ./...

clean:
	rm -r build/
