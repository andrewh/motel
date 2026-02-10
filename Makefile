# ABOUTME: Makefile for motel project with comprehensive verification and build tasks
# ABOUTME: Provides targets for testing, verification, building, and deployment automation

# Variables
BINARY_NAME=motel
CLI_BINARY_NAME=motelier
LLM_BINARY_NAME=motel-llm
MODULE=github.com/andrewh/motel
BUILD_DIR=build
INSTALL_DIR=$(GOPATH)/bin
SOURCE_DIR=./cmd/motel
CLI_SOURCE_DIR=./cmd/motelier
LLM_SOURCE_DIR=./cmd/motel-llm

# Version information
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "v0.1.0")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME ?= $(shell date -u '+%Y-%m-%d %H:%M:%S UTC')

# Build flags
LDFLAGS = -X 'main.version=$(VERSION)' \
          -X 'main.commit=$(COMMIT)' \
          -X 'main.buildTime=$(BUILD_TIME)'
CLI_LDFLAGS = -X 'main.version=$(VERSION)' \
              -X 'main.commit=$(COMMIT)' \
              -X 'main.buildTime=$(BUILD_TIME)'
D2 := d2
DIAGRAMS := architecture.d2 dataflow.d2 state.d2 handler-registry.d2
DIAGRAMS_DIR := diagrams
D2_FILES := $(wildcard $(DIAGRAMS_DIR)/*.d2)
SVG_FILES := $(patsubst %.d2,%.svg,$(D2_FILES))



.PHONY: all help build build-llm install install-binaries install-manpages test test-unit test-integration lint clean run dev docker-build docker-run setup teardown deb-package apk-package install-manpages-macos diagrams kill pre-commit pre-commit-install pre-commit-run pre-commit-update version set-version tag-release install-tools check-tools

# Default target - show help when running 'make' with no arguments
.DEFAULT_GOAL := help

# Standard aggregate target for makefile linters
all: build-all ## Build both binaries (aggregate)

# Help target
help: ## Show this help message
	@echo "Motel Project - Available Make Targets"
	@echo "======================================"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Build targets
build: ## Build the motel binary
	@echo "Building motel... Version=$(VERSION) Commit=$(COMMIT) Time=$(BUILD_TIME)"; \
	mkdir -p $(BUILD_DIR); \
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR); \
	echo "✓ Build complete: ./$(BUILD_DIR)/$(BINARY_NAME)"

build-cli: generate ## Build the motelier CLI binary
	@echo "Building motelier... Version=$(VERSION) Commit=$(COMMIT) Time=$(BUILD_TIME)"; \
	mkdir -p $(BUILD_DIR); \
	go build -ldflags "$(CLI_LDFLAGS)" -o $(BUILD_DIR)/$(CLI_BINARY_NAME) $(CLI_SOURCE_DIR); \
	echo "✓ CLI build complete: ./$(BUILD_DIR)/$(CLI_BINARY_NAME)"

build-llm: ## Build the motel-llm CLI binary
	@echo "Building motel-llm... Version=$(VERSION) Commit=$(COMMIT) Time=$(BUILD_TIME)"; \
	mkdir -p $(BUILD_DIR); \
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(LLM_BINARY_NAME) $(LLM_SOURCE_DIR); \
	echo "✓ LLM CLI build complete: ./$(BUILD_DIR)/$(LLM_BINARY_NAME)"

build-all: build build-cli build-llm ## Build all binaries

build-docker: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t motel .
	@echo "✓ Docker image built: motel"

install: build-all install-binaries install-manpages ## Build and install both binaries and manpages

install-binaries: ## Install binaries to ~/bin
	@mkdir -p $(INSTALL_DIR)
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@cp $(BUILD_DIR)/$(CLI_BINARY_NAME) $(INSTALL_DIR)/$(CLI_BINARY_NAME)
	@cp $(BUILD_DIR)/$(LLM_BINARY_NAME) $(INSTALL_DIR)/$(LLM_BINARY_NAME)
	@echo "✓ Installed $(BINARY_NAME), $(CLI_BINARY_NAME) and $(LLM_BINARY_NAME) to $(INSTALL_DIR)"

install-manpages: ## Install manpages (macOS only)
	@if [ "$$(uname -s)" = "Darwin" ]; then $(MAKE) install-manpages-macos; fi

# Test targets
test: ## Run all tests (unit + integration)
	@echo "Running all tests..."
	@$(MAKE) test-unit
	@$(MAKE) test-integration
	@echo "✓ All tests passed"

test-unit: ## Run unit tests only (fast, parallel)
	@echo "Running unit tests..."
	@go fmt ./...
	@go test -v ./...
	@echo "✓ Unit tests passed"

test-integration: ## Run integration tests only (requires database)
	@echo "Running integration tests..."
	@go test -tags=integration -v -p 1 ./pkg/service/... ./pkg/app/...
	@echo "✓ Integration tests passed"

# Code generation targets
generate: ## Generate code from OpenAPI spec
	@echo "Generating client models..."; \
	command -v oapi-codegen >/dev/null 2>&1 && oapi-codegen -config oapi-codegen.yaml -o cmd/motelier/pkg/generated/models.go cmd/motel/api/openapi.yaml && echo "✓ Client models generated" || (echo "▲  Install: go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen" && exit 1)

sqlc-generate: ## Generate PostgreSQL (pkg/db) and SQLite (pkg/dbsqlite) query code via sqlc
	@echo "Generating sqlc code (PostgreSQL + SQLite)..."
	@if command -v sqlc >/dev/null 2>&1; then \
		sqlc generate; \
		echo "✓ sqlc code generated"; \
	else \
		echo "❌ sqlc not installed. Install with: brew install sqlc (macOS) or go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest"; \
		exit 1; \
	fi

generate-check: ## Check if generated code is up to date
	@echo "Checking if generated code is up to date..."
	@tmp_file=$$(mktemp); \
	oapi-codegen -config oapi-codegen.yaml cmd/motel/api/openapi.yaml > "$$tmp_file"; \
	if ! diff -q "$$tmp_file" cmd/motelier/pkg/generated/models.go >/dev/null 2>&1; then \
		echo "❌ Generated code is out of date. Run 'make generate' to update."; \
		rm "$$tmp_file"; \
		exit 1; \
	else \
		echo "✓ Generated code is up to date"; \
		rm "$$tmp_file"; \
	fi

# Code quality targets
lint: ## Run linting
	@echo "Running linting..."; \
	go fmt ./...; \
	go vet ./...; \
	(command -v golangci-lint >/dev/null 2>&1 && golangci-lint run) || echo "▲  golangci-lint not installed, skipping"; \
	echo "✓ Linting complete"

fmt: ## Format code
	@echo "Formatting code..."
	go fmt ./...
	@echo "✓ Code formatted"

# Development targets
dev: ## Build both binaries with race detection
	@echo "Building with race detection..."
	@mkdir -p $(BUILD_DIR)
	go build -race -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	go build -race -ldflags "$(CLI_LDFLAGS)" -o $(BUILD_DIR)/$(CLI_BINARY_NAME) $(CLI_SOURCE_DIR)
	@echo "✓ Development build complete for both binaries"

run: build ## Build and run the server
	@echo "Starting motel server..."
	@if [ -z "$(DATABASE_URL)" ]; then \
		export DATABASE_URL="postgres://localhost:5432/motel?sslmode=disable"; \
	fi
	./$(BUILD_DIR)/$(BINARY_NAME)

run-docker: build-docker ## Build and run Docker container
	@echo "Running Docker container..."
	docker run -it --rm \
		-p 8080:8080 \
		-e DATABASE_URL="$${DATABASE_URL:-postgres://host.docker.internal:5432/motel?sslmode=disable}" \
		motel

# Database targets
db-setup: ## Setup database (create databases and run migrations)
	@echo "Setting up databases..."
	@psql postgres -c "CREATE DATABASE motel;" 2>/dev/null || echo "motel database already exists"
	@psql postgres -c "CREATE DATABASE motel_test;" 2>/dev/null || echo "motel_test database already exists"
	@if command -v migrate >/dev/null 2>&1; then \
		migrate -path migrations -database "$${DATABASE_URL:-postgres://localhost:5432/motel?sslmode=disable}" up; \
		migrate -path migrations -database "$${DATABASE_URL:-postgres://localhost:5432/motel_test?sslmode=disable}" up; \
		echo "✓ Migrations applied"; \
	else \
		echo "▲  migrate command not found. Install with: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"; \
	fi

db-reset: ## Reset database (drop and recreate)
	@echo "Resetting databases..."
	@psql postgres -c "DROP DATABASE IF EXISTS motel;"
	@psql postgres -c "DROP DATABASE IF EXISTS motel_test;"
	@$(MAKE) db-setup

# Utility targets
clean: ## Clean build artifacts and temporary files
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)/
	rm -rf coverage/
	go clean
	@echo "✓ Clean complete"

setup: ## Setup development environment
	@echo "Setting up dev env..."; go mod download; mkdir -p $(BUILD_DIR); $(MAKE) db-setup; [ -f scripts/setup_pre_commit.sh ] && ./scripts/setup_pre_commit.sh || echo "⚠️  Pre-commit setup script not found"; echo "✓ Development environment ready"

teardown: ## Teardown development environment
	@echo "Tearing down development environment..."
	@$(MAKE) clean
	@$(MAKE) db-reset
	@echo "✓ Environment cleaned up"

# Tool installation targets
check-tools: ## Check if required development tools are installed
	@echo "Checking required development tools..."
	@echo ""
	@missing_tools=""; \
	for t in oapi-codegen sqlc golangci-lint migrate pre-commit; do \
		echo "Checking $$t..."; \
		if ! command -v $$t >/dev/null 2>&1; then \
			echo "  ❌ $$t not found"; \
			missing_tools="$$missing_tools $$t"; \
		else \
			echo "  ✓ $$t installed"; \
		fi; \
	done; \
	echo ""; \
	if [ -n "$$missing_tools" ]; then \
		echo "Missing tools:$$missing_tools"; \
		echo ""; \
		echo "Run 'make install-tools' to install missing tools"; \
		exit 1; \
	else \
		echo "✓ All required tools are installed"; \
	fi

install-tools: ## Install required development tools
	@bash ./scripts/install-tools.sh


# CI/CD targets
ci-test: ## Run tests suitable for CI environment
	@echo "Running CI tests..."
	@echo "Running unit tests with race detection..."
	go test -race -coverprofile=coverage-unit.out ./...
	@echo "Running integration tests with coverage..."
	go test -tags=integration -race -coverprofile=coverage-integration.out -p 1 ./pkg/service/... ./pkg/app/...
	@echo "Generating coverage report..."
	go tool cover -html=coverage-unit.out -o coverage-unit.html
	go tool cover -html=coverage-integration.out -o coverage-integration.html
	@echo "✓ CI tests complete"

ci-build: ## Build for CI environment
	@echo "Building for CI..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s $(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	@echo "✓ CI build complete"

# Quick commands for common workflows
quick-test: ## Quick unit tests and lint (fast feedback)
	@$(MAKE) test-unit lint

full-test: ## Run all tests and lint
	@$(MAKE) test lint

# Package targets
deb-package: ## Build Debian package
	@echo "Building Debian package..."
	./deployments/build-deb.sh
	@echo "✓ Debian package built"

apk-package: ## Build Alpine Linux package (requires Alpine Linux system)
	@echo "Building Alpine Linux package..."
	./deployments/build-apk.sh
	@echo "✓ Alpine Linux package built"

install-manpages-macos: ## Install manpages into a macOS manpath
	./scripts/install_manpages_macos.sh --prefix $$HOME/.local

diagrams: $(SVG_FILES)

$(DIAGRAMS_DIR)/%.svg: $(DIAGRAMS_DIR)/%.d2
	$(D2) $< $@

kill: ## Kill any running motel servers
	@lsof -t -i:8080 | xargs kill

# Pre-commit targets
pre-commit-install: ## Install and setup pre-commit hooks
	@echo "Installing pre-commit hooks..."
	@if [ -f scripts/setup_pre_commit.sh ]; then \
		./scripts/setup_pre_commit.sh; \
	else \
		echo "❌ Pre-commit setup script not found"; \
		exit 1; \
	fi

pre-commit-run: ## Run pre-commit hooks on all files
	@echo "Running pre-commit hooks on all files..."
	@if command -v pre-commit >/dev/null 2>&1; then \
		pre-commit run --all-files; \
	else \
		echo "❌ pre-commit not installed. Run 'make pre-commit-install' first."; \
		exit 1; \
	fi

pre-commit-update: ## Update pre-commit hooks to latest versions
	@echo "Updating pre-commit hooks..."
	@if command -v pre-commit >/dev/null 2>&1; then \
		pre-commit autoupdate; \
	else \
		echo "❌ pre-commit not installed. Run 'make pre-commit-install' first."; \
		exit 1; \
	fi

pre-commit: pre-commit-run ## Alias for pre-commit-run

# Version management targets
version: ## Show current version information
	@echo "VERSION=$(VERSION) COMMIT=$(COMMIT) BUILD_TIME=$(BUILD_TIME)"; \
	echo "Git Status:"; git status --porcelain 2>/dev/null | head -5 || echo "No git repo"; \
	([ -x "$(BUILD_DIR)/$(BINARY_NAME)" ] && ./$(BUILD_DIR)/$(BINARY_NAME) version || echo "(build first: make build)"); \
	([ -x "$(BUILD_DIR)/$(CLI_BINARY_NAME)" ] && ./$(BUILD_DIR)/$(CLI_BINARY_NAME) version || echo "(build first: make build-cli)")

set-version: ## Set a new version tag (usage: make set-version VERSION=v1.2.3)
	@if [ -z "$(VERSION)" ]; then \
		echo "❌ VERSION is required. Usage: make set-version VERSION=v1.2.3"; \
		exit 1; \
	fi
	@if ! echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9-]+)?$$'; then \
		echo "❌ Invalid version format. Expected: v1.2.3 or v1.2.3-beta"; \
		echo "   Provided: $(VERSION)"; \
		exit 1; \
	fi
	@echo "Setting version to $(VERSION)..."
	@if git rev-parse "$(VERSION)" >/dev/null 2>&1; then \
		echo "❌ Tag $(VERSION) already exists"; \
		exit 1; \
	fi
	@if ! git diff-index --quiet HEAD --; then \
		echo "❌ Working directory is not clean. Please commit your changes first."; \
		echo "Uncommitted changes:"; \
		git status --porcelain; \
		exit 1; \
	fi
	@echo "Creating and pushing tag $(VERSION)..."
	git tag -a "$(VERSION)" -m "Release $(VERSION)"
	git push origin "$(VERSION)"
	@echo "✓ Version $(VERSION) tagged and pushed"
	@echo ""
	@echo "To create a release, the GitHub Actions workflow will trigger automatically."
	@echo "You can also manually trigger it at: https://github.com/andrewh/motel/actions/workflows/release.yml"

tag-release: ## Create a release tag with current version (interactive)
	@echo "Creating a new release tag..."
	@echo ""
	@echo "Current version: $(VERSION)"
	@echo ""
	@echo "Please enter the new version (e.g., v1.2.3 or v1.2.3-beta):"
	@read -p "Version: " NEW_VERSION; \
	if [ -n "$$NEW_VERSION" ]; then \
		$(MAKE) set-version VERSION=$$NEW_VERSION; \
	else \
		echo "❌ No version provided. Cancelling."; \
	fi
