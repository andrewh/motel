VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: build test lint site clean

build:
	go build -ldflags "$(LDFLAGS)" -o build/motel ./cmd/motel

test:
	go test $(shell go list ./... | grep -v /third_party/)

lint:
	@test -z "$$(gofmt -s -l .)" || (gofmt -s -l . && exit 1)
	go vet ./...

site:
	rm -rf .site site
	mkdir -p .site/cmd/motel
	cp README.md .site/index.md
	cp LICENSE .site/LICENSE
	cp -R docs .site/docs
	cp cmd/motel/README.md .site/cmd/motel/README.md
	mkdocs build --strict

clean:
	rm -r build/
