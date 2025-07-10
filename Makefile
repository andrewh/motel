# Makefile for beacon project

# Variables
BINARY_NAME=beacon
MODULE=github.com/andrewh/beacon
BUILD_DIR=build
INSTALL_DIR=$(HOME)/bin
SOURCE_DIR=./cmd/beacon

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build:
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)

# Install to ~/bin
.PHONY: install
install: build
	@mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@echo "Installed $(BINARY_NAME) to $(INSTALL_DIR)"

# Clean build artifacts
.PHONY: clean
clean:
	rm -rf $(BUILD_DIR)

# Run tests
.PHONY: test
test:
	./scripts/run_tests.sh

# Run linting
.PHONY: lint
lint:
	go fmt ./...
	golangci-lint run

# Development build with race detection
.PHONY: dev
dev:
	@mkdir -p $(BUILD_DIR)
	go build -race -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)

# Show help
.PHONY: help
help:
	@echo "Available targets:"
	@echo "  build    - Build the beacon binary to build/beacon"
	@echo "  install  - Build and install to ~/bin/beacon"
	@echo "  clean    - Remove build artifacts"
	@echo "  test     - Run all tests"
	@echo "  lint     - Run formatting and linting"
	@echo "  dev      - Build with race detection"
	@echo "  help     - Show this help message"
