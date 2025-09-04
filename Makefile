# ABOUTME: Makefile for beacon project with comprehensive verification and build tasks
# ABOUTME: Provides targets for testing, verification, building, and deployment automation

# Variables
BINARY_NAME=beacon
CLI_BINARY_NAME=beaconctl
MODULE=github.com/andrewh/beacon
BUILD_DIR=build
INSTALL_DIR=$(GOPATH)/bin
SOURCE_DIR=./cmd/beacon
CLI_SOURCE_DIR=./cmd/beaconctl

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



.PHONY: help build test test-unit test-integration test-perf test-perf-simple test-perf-endpoints test-perf-brief test-perf-final test-perf-all lint clean verify verify-all verify-api verify-docker verify-database verify-coverage verify-completeness run dev docker-build docker-run setup teardown deb-package apk-package diagrams kill pre-commit pre-commit-install pre-commit-run pre-commit-update version set-version tag-release

# Default target
help: ## Show this help message
	@echo "Beacon Project - Available Make Targets"
	@echo "======================================"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Environment Variables:"
	@echo "  DATABASE_URL     PostgreSQL connection string (default: postgres://localhost:5432/beacon?sslmode=disable)"
	@echo "  PORT            Server port (default: 8080)"
	@echo "  MIN_COVERAGE    Minimum test coverage percentage (default: 40)"
	@echo ""
	@echo "Build Targets:"
	@echo "  build            Build beacon server binary"
	@echo "  build-cli        Build beaconctl CLI binary"
	@echo "  build-all        Build both binaries"
	@echo "  install          Build and install both binaries to ~/bin"
	@echo ""
	@echo "Performance Testing:"
	@echo "  test-perf-*      Requires running server (start with: make run)"
	@echo "  Example:         make run & sleep 3 && make test-perf-all"
	@echo ""
	@echo "Code Quality:"
	@echo "  pre-commit-install  Setup pre-commit hooks and tools"
	@echo "  pre-commit          Run all pre-commit hooks"
	@echo "  lint                Run Go linting (gofmt, govet, golangci-lint)"
	@echo ""
	@echo "Version Management:"
	@echo "  version             Show current version information"
	@echo "  set-version         Set new version tag (make set-version VERSION=v1.2.3)"
	@echo "  tag-release         Interactive version tagging for releases"

# Build targets
build: ## Build the beacon binary
	@echo "Building beacon binary..."
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build time: $(BUILD_TIME)"
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	@echo "✓ Build complete: ./$(BUILD_DIR)/$(BINARY_NAME)"

build-cli: generate ## Build the beaconctl CLI binary
	@echo "Building beaconctl CLI binary..."
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build time: $(BUILD_TIME)"
	@mkdir -p $(BUILD_DIR)
	go build -ldflags "$(CLI_LDFLAGS)" -o $(BUILD_DIR)/$(CLI_BINARY_NAME) $(CLI_SOURCE_DIR)
	@echo "✓ CLI build complete: ./$(BUILD_DIR)/$(CLI_BINARY_NAME)"

build-all: build build-cli ## Build both beacon and beaconctl binaries

build-docker: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t beacon .
	@echo "✓ Docker image built: beacon"

install: build-all ## Build and install both binaries to ~/bin
	@mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	cp $(BUILD_DIR)/$(CLI_BINARY_NAME) $(INSTALL_DIR)/$(CLI_BINARY_NAME)
	@echo "✓ Installed $(BINARY_NAME) and $(CLI_BINARY_NAME) to $(INSTALL_DIR)"

# Test targets
test: ## Run all tests (unit + integration)
	@echo "Running all tests..."
	./scripts/run_tests.sh
	@echo "✓ All tests passed"

test-unit: ## Run unit tests only (fast, parallel)
	@echo "Running unit tests..."
	./scripts/test_unit.sh
	@echo "✓ Unit tests passed"

test-integration: ## Run integration tests only (requires database)
	@echo "Running integration tests..."
	./scripts/test_integration.sh
	@echo "✓ Integration tests passed"

test-verbose: ## Run tests with verbose output
	@echo "Running tests with verbose output..."
	go test -v ./...

# Performance testing targets (requires running server)
test-perf: ## Run comprehensive performance API tests
	@echo "Running comprehensive performance API tests..."
	./scripts/test_performance_api.sh
	@echo "✓ Performance API tests passed"

test-perf-simple: ## Run simple performance test validation
	@echo "Running simple performance test..."
	./scripts/test_simple_performance_fixed.sh
	@echo "✓ Simple performance test passed"

test-perf-endpoints: ## Test performance API endpoints
	@echo "Testing performance API endpoints..."
	./scripts/test_perf_endpoints.sh
	@echo "✓ Performance endpoints test passed"

test-perf-brief: ## Test brief API endpoint names
	@echo "Testing brief API endpoint names..."
	./scripts/test_brief_api.sh
	@echo "✓ Brief API test passed"

test-perf-final: ## Run final performance testing validation
	@echo "Running final performance testing validation..."
	./scripts/test_final_performance.sh
	@echo "✓ Final performance validation passed"

test-perf-all: ## Run all performance tests (requires running server)
	@echo "Running all performance tests..."
	@echo "================================"
	@$(MAKE) test-perf-brief
	@echo ""
	@$(MAKE) test-perf-endpoints
	@echo ""
	@$(MAKE) test-perf-simple
	@echo ""
	@$(MAKE) test-perf-final
	@echo ""
	@$(MAKE) test-perf
	@echo ""
	@echo "✓ All performance tests passed"

# Code generation targets
generate: ## Generate code from OpenAPI spec
	@echo "Generating client models from OpenAPI spec..."
	@if command -v oapi-codegen >/dev/null 2>&1; then \
		oapi-codegen -config oapi-codegen.yaml -o cmd/beaconctl/pkg/generated/models.go cmd/beacon/api/openapi.yaml; \
		echo "✓ Client models generated"; \
	else \
		echo "▲  oapi-codegen not installed. Install with: go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"; \
		exit 1; \
	fi

generate-check: ## Check if generated code is up to date
	@echo "Checking if generated code is up to date..."
	@tmp_file=$$(mktemp); \
	oapi-codegen -config oapi-codegen.yaml cmd/beacon/api/openapi.yaml > "$$tmp_file"; \
	if ! diff -q "$$tmp_file" cmd/beaconctl/pkg/generated/models.go >/dev/null 2>&1; then \
		echo "❌ Generated code is out of date. Run 'make generate' to update."; \
		rm "$$tmp_file"; \
		exit 1; \
	else \
		echo "✓ Generated code is up to date"; \
		rm "$$tmp_file"; \
	fi

# Code quality targets
lint: ## Run linting
	@echo "Running linting..."
	go fmt ./...
	go vet ./...
	# Enhanced golangci-lint configuration via .golangci.yml (compatible with v2.3.0+)
	# Focuses on: error handling, security, resource management, basic quality
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "▲  golangci-lint not installed, skipping advanced linting"; \
	fi
	@echo "✓ Linting complete"

fmt: ## Format code
	@echo "Formatting code..."
	go fmt ./...
	@echo "✓ Code formatted"

# Verification targets
verify-api: ## Verify API implementation
	@echo "Verifying API implementation..."
	./scripts/verify_api.sh

verify-docker: ## Verify Docker implementation
	@echo "Verifying Docker implementation..."
	./scripts/verify_docker.sh

verify-database: ## Verify database schema
	@echo "Verifying database schema..."
	./scripts/verify_database.sh

verify-coverage: ## Analyze test coverage
	@echo "Analyzing test coverage..."
	./scripts/verify_coverage.sh

verify-completeness: ## Run comprehensive completeness check
	@echo "Running completeness verification..."
	./scripts/verify_completeness.sh

verify: verify-completeness ## Run basic verification (alias for verify-completeness)

verify-all: ## Run all verification checks
	@echo "Running all verification checks..."
	@echo "=================================="
	@$(MAKE) verify-completeness
	@echo ""
	@echo "Note: Run individual verification commands for detailed analysis:"
	@echo "  make verify-api        (requires running server)"
	@echo "  make verify-docker     (requires Docker)"
	@echo "  make verify-database   (requires PostgreSQL)"
	@echo "  make verify-coverage   (generates detailed reports)"

# Development targets
dev: ## Build both binaries with race detection
	@echo "Building with race detection..."
	@mkdir -p $(BUILD_DIR)
	go build -race -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	go build -race -ldflags "$(CLI_LDFLAGS)" -o $(BUILD_DIR)/$(CLI_BINARY_NAME) $(CLI_SOURCE_DIR)
	@echo "✓ Development build complete for both binaries"

run: build ## Build and run the server
	@echo "Starting beacon server..."
	@if [ -z "$(DATABASE_URL)" ]; then \
		export DATABASE_URL="postgres://localhost:5432/beacon?sslmode=disable"; \
	fi
	./$(BUILD_DIR)/$(BINARY_NAME)

run-docker: build-docker ## Build and run Docker container
	@echo "Running Docker container..."
	docker run -it --rm \
		-p 8080:8080 \
		-e DATABASE_URL="$${DATABASE_URL:-postgres://host.docker.internal:5432/beacon?sslmode=disable}" \
		beacon

# Database targets
db-setup: ## Setup database (create databases and run migrations)
	@echo "Setting up databases..."
	@psql postgres -c "CREATE DATABASE beacon;" 2>/dev/null || echo "beacon database already exists"
	@psql postgres -c "CREATE DATABASE beacon_test;" 2>/dev/null || echo "beacon_test database already exists"
	@if command -v migrate >/dev/null 2>&1; then \
		migrate -path migrations -database "$${DATABASE_URL:-postgres://localhost:5432/beacon?sslmode=disable}" up; \
		migrate -path migrations -database "$${DATABASE_URL:-postgres://localhost:5432/beacon_test?sslmode=disable}" up; \
		echo "✓ Migrations applied"; \
	else \
		echo "▲  migrate command not found. Install with: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"; \
	fi

db-reset: ## Reset database (drop and recreate)
	@echo "Resetting databases..."
	@psql postgres -c "DROP DATABASE IF EXISTS beacon;"
	@psql postgres -c "DROP DATABASE IF EXISTS beacon_test;"
	@$(MAKE) db-setup

# Utility targets
clean: ## Clean build artifacts and temporary files
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)/
	rm -rf coverage/
	go clean
	@echo "✓ Clean complete"

setup: ## Setup development environment
	@echo "Setting up development environment..."
	@echo "Installing dependencies..."
	go mod download
	@echo "Creating build directory..."
	mkdir -p $(BUILD_DIR)
	@echo "Setting up databases..."
	@$(MAKE) db-setup
	@echo "Setting up pre-commit hooks..."
	@if [ -f scripts/setup_pre_commit.sh ]; then \
		./scripts/setup_pre_commit.sh; \
	else \
		echo "⚠️  Pre-commit setup script not found"; \
	fi
	@echo "✓ Development environment ready"

teardown: ## Teardown development environment
	@echo "Tearing down development environment..."
	@$(MAKE) clean
	@$(MAKE) db-reset
	@echo "✓ Environment cleaned up"

# CI/CD targets
ci-test: ## Run tests suitable for CI environment
	@echo "Running CI tests..."
	@echo "Running unit tests with race detection..."
	go test -race -coverprofile=coverage-unit.out ./internal/pkg/query/... ./internal/pkg/models/... ./internal/pkg/executor/handlers/... ./internal/pkg/validation/... ./internal/pkg/config/... ./internal/pkg/metrics/...
	@echo "Running integration tests with coverage..."
	go test -tags=integration -race -coverprofile=coverage-integration.out -p 1 ./internal/pkg/service/...
	go test -race -coverprofile=coverage-http.out -p 1 ./internal/app/beacon/...
	go test -race -coverprofile=coverage-main.out ./cmd/beacon/...
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

quick-verify: ## Quick verification (completeness check only)
	@$(MAKE) verify-completeness

full-verify: ## Full verification suite (requires all dependencies)
	@echo "Running full verification suite..."
	@$(MAKE) lint test verify-completeness
	@echo ""
	@echo "Test execution summary:"
	@echo "• Unit tests: Fast parallel execution (~0.8s)"
	@echo "• Integration tests: Sequential with database isolation (~15s)"
	@echo ""
	@echo "For complete verification including live services:"
	@echo "1. Start the server: make run"
	@echo "2. In another terminal: make verify-api"
	@echo "3. For Docker verification: make verify-docker"
	@echo "4. For database verification: make verify-database"
	@echo "5. For detailed coverage: make verify-coverage"

# Package targets
deb-package: ## Build Debian package
	@echo "Building Debian package..."
	./deployments/build-deb.sh
	@echo "✓ Debian package built"

apk-package: ## Build Alpine Linux package (requires Alpine Linux system)
	@echo "Building Alpine Linux package..."
	./deployments/build-apk.sh
	@echo "✓ Alpine Linux package built"

diagrams: $(SVG_FILES)

$(DIAGRAMS_DIR)/%.svg: $(DIAGRAMS_DIR)/%.d2
	$(D2) $< $@

kill: ## Kill any running beacon servers
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
	@echo "Current Version Information:"
	@echo "============================"
	@echo "VERSION:    $(VERSION)"
	@echo "COMMIT:     $(COMMIT)"
	@echo "BUILD_TIME: $(BUILD_TIME)"
	@echo ""
	@echo "Git Status:"
	@git status --porcelain 2>/dev/null | head -5 || echo "No git repository or changes"
	@echo ""
	@if [ -x "$(BUILD_DIR)/$(BINARY_NAME)" ]; then \
		echo "Built beacon version:"; \
		./$(BUILD_DIR)/$(BINARY_NAME) --version 2>/dev/null || echo "Binary not built or not executable"; \
	else \
		echo "Beacon binary not found. Run 'make build' first."; \
	fi
	@echo ""
	@if [ -x "$(BUILD_DIR)/$(CLI_BINARY_NAME)" ]; then \
		echo "Built beaconctl version:"; \
		./$(BUILD_DIR)/$(CLI_BINARY_NAME) version 2>/dev/null || echo "CLI binary not built or not executable"; \
	else \
		echo "Beaconctl binary not found. Run 'make build-cli' first."; \
	fi

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
	@echo "You can also manually trigger it at: https://github.com/andrewh/beacon-go/actions/workflows/release.yml"

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
