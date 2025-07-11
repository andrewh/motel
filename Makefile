# ABOUTME: Makefile for beacon project with comprehensive verification and build tasks
# ABOUTME: Provides targets for testing, verification, building, and deployment automation

# Variables
BINARY_NAME=beacon
MODULE=github.com/andrewh/beacon
BUILD_DIR=build
INSTALL_DIR=$(GOPATH)/bin
SOURCE_DIR=./cmd/beacon

.PHONY: help build test lint clean verify verify-all verify-api verify-docker verify-database verify-coverage verify-completeness run dev docker-build docker-run setup teardown

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

# Build targets
build: ## Build the beacon binary
	@echo "Building beacon binary..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	@echo "✅ Build complete: ./$(BUILD_DIR)/$(BINARY_NAME)"

build-docker: ## Build Docker image
	@echo "Building Docker image..."
	docker build -t beacon .
	@echo "✅ Docker image built: beacon"

install: build ## Build and install to ~/bin
	@mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(BINARY_NAME) $(INSTALL_DIR)/$(BINARY_NAME)
	@echo "✅ Installed $(BINARY_NAME) to $(INSTALL_DIR)"

# Test targets
test: ## Run all tests
	@echo "Running tests..."
	./scripts/run_tests.sh
	@echo "✅ All tests passed"

test-verbose: ## Run tests with verbose output
	@echo "Running tests with verbose output..."
	go test -v ./...

test-integration: ## Run integration tests only
	@echo "Running integration tests..."
	go test ./tests/integration/...

# Code quality targets
lint: ## Run linting
	@echo "Running linting..."
	go fmt ./...
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "⚠️  golangci-lint not installed, skipping advanced linting"; \
	fi
	@echo "✅ Linting complete"

fmt: ## Format code
	@echo "Formatting code..."
	go fmt ./...
	@echo "✅ Code formatted"

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
dev: ## Build with race detection
	@echo "Building with race detection..."
	@mkdir -p $(BUILD_DIR)
	go build -race -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	@echo "✅ Development build complete"

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
		echo "✅ Migrations applied"; \
	else \
		echo "⚠️  migrate command not found. Install with: go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest"; \
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
	@echo "✅ Clean complete"

setup: ## Setup development environment
	@echo "Setting up development environment..."
	@echo "Installing dependencies..."
	go mod download
	@echo "Creating build directory..."
	mkdir -p $(BUILD_DIR)
	@echo "Setting up databases..."
	@$(MAKE) db-setup
	@echo "✅ Development environment ready"

teardown: ## Teardown development environment
	@echo "Tearing down development environment..."
	@$(MAKE) clean
	@$(MAKE) db-reset
	@echo "✅ Environment cleaned up"

# CI/CD targets
ci-test: ## Run tests suitable for CI environment
	@echo "Running CI tests..."
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "✅ CI tests complete"

ci-build: ## Build for CI environment
	@echo "Building for CI..."
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o $(BUILD_DIR)/$(BINARY_NAME) $(SOURCE_DIR)
	@echo "✅ CI build complete"

# Quick commands for common workflows
quick-test: ## Quick test and lint
	@$(MAKE) test lint

quick-verify: ## Quick verification (completeness check only)
	@$(MAKE) verify-completeness

full-verify: ## Full verification suite (requires all dependencies)
	@echo "Running full verification suite..."
	@$(MAKE) lint test verify-completeness
	@echo ""
	@echo "For complete verification including live services:"
	@echo "1. Start the server: make run"
	@echo "2. In another terminal: make verify-api"
	@echo "3. For Docker verification: make verify-docker"
	@echo "4. For database verification: make verify-database"
	@echo "5. For detailed coverage: make verify-coverage"
