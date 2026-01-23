# Makefile for Consonant System
#
# This Makefile provides convenient targets for building, testing, and running
# the Consonant system during development and deployment.
#
# Common commands:
#   make dev          - Start infrastructure and run backend locally
#   make build        - Build all components
#   make test         - Run all tests
#   make clean        - Clean build artifacts
#   make help         - Show available targets

.PHONY: help
help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Available targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# =============================================================================
# DEVELOPMENT
# =============================================================================

.PHONY: dev
dev: infra-up backend-run ## Start full development environment

.PHONY: infra-up
infra-up: ## Start PostgreSQL and Redis
	@echo "Starting infrastructure..."
	docker-compose up -d postgres redis
	@echo "Waiting for services to be healthy..."
	@sleep 5
	docker-compose ps
	@echo "Infrastructure ready!"

.PHONY: infra-down
infra-down: ## Stop infrastructure
	@echo "Stopping infrastructure..."
	docker-compose down

.PHONY: infra-clean
infra-clean: ## Stop infrastructure and remove volumes (fresh start)
	@echo "Cleaning infrastructure..."
	docker-compose down -v
	@echo "All data removed. Run 'make infra-up' for fresh start."

.PHONY: infra-logs
infra-logs: ## Follow infrastructure logs
	docker-compose logs -f postgres redis

# =============================================================================
# BACKEND
# =============================================================================

.PHONY: backend-deps
backend-deps: ## Download Go dependencies
	@echo "Downloading Go dependencies..."
	cd backend && go mod download
	cd backend && go mod tidy

.PHONY: backend-generate
backend-generate: ## Generate protobuf code
	@echo "Generating protobuf code..."
	@echo "Note: Requires protoc and protoc-gen-go-grpc installed"
	@echo "Install with: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest"
	@echo "Install with: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest"
	cd backend && protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		proto/balance/v1/balance.proto

.PHONY: backend-build
backend-build: ## Build backend binary
	@echo "Building backend..."
	cd backend && go build -o bin/api ./cmd/api
	@echo "Backend built: backend/bin/api"

.PHONY: backend-run
backend-run: ## Run backend locally (requires infra-up first)
	@echo "Starting backend..."
	cd backend && \
		GRPC_PORT=9090 \
		HTTP_PORT=8080 \
		REDIS_ADDR=localhost:6379 \
		POSTGRES_URL="postgres://postgres:postgres@localhost:5432/consonant?sslmode=disable" \
		LOG_LEVEL=debug \
		ENVIRONMENT=development \
		./bin/api

.PHONY: backend-test
backend-test: ## Run backend tests
	@echo "Running backend tests..."
	cd backend && go test -v -race ./...

.PHONY: backend-test-coverage
backend-test-coverage: ## Run backend tests with coverage
	@echo "Running backend tests with coverage..."
	cd backend && go test -v -race -coverprofile=coverage.out ./...
	cd backend && go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: backend/coverage.html"

.PHONY: backend-lint
backend-lint: ## Run linters on backend code
	@echo "Running linters..."
	cd backend && go vet ./...
	cd backend && golangci-lint run ./...

.PHONY: backend-format
backend-format: ## Format backend code
	@echo "Formatting code..."
	cd backend && go fmt ./...
	cd backend && goimports -w .

# =============================================================================
# DATABASE
# =============================================================================

.PHONY: db-connect
db-connect: ## Connect to PostgreSQL
	docker-compose exec postgres psql -U postgres -d consonant

.PHONY: db-migrate
db-migrate: ## Run database migrations
	docker-compose exec postgres psql -U postgres -d consonant -f /docker-entrypoint-initdb.d/001_initial_schema.up.sql

.PHONY: db-reset
db-reset: infra-clean infra-up ## Reset database to fresh state
	@echo "Database reset complete"

.PHONY: db-check
db-check: ## Check database integrity
	docker-compose exec postgres psql -U postgres -d consonant -c "SELECT * FROM verify_balance_integrity('test_customer_1');"

# =============================================================================
# REDIS
# =============================================================================

.PHONY: redis-cli
redis-cli: ## Connect to Redis CLI
	docker-compose exec redis redis-cli

.PHONY: redis-monitor
redis-monitor: ## Monitor Redis commands in real-time
	docker-compose exec redis redis-cli MONITOR

.PHONY: redis-stats
redis-stats: ## Show Redis statistics
	docker-compose exec redis redis-cli INFO stats

# =============================================================================
# TESTING & VALIDATION
# =============================================================================

.PHONY: test-balance
test-balance: ## Test balance check with grpcurl
	@echo "Testing balance check..."
	grpcurl -plaintext \
		-H "authorization: Bearer consonant_test_key_1234567890" \
		-d '{"customer_id": "test_customer_1"}' \
		localhost:9090 consonant.balance.v1.BalanceService/GetBalance

.PHONY: test-health
test-health: ## Test health endpoint
	@echo "Testing health endpoint..."
	curl -s http://localhost:8080/health
	@echo ""

.PHONY: test-ready
test-ready: ## Test readiness endpoint
	@echo "Testing readiness endpoint..."
	curl -s http://localhost:8080/ready
	@echo ""

.PHONY: test-metrics
test-metrics: ## View Prometheus metrics
	@echo "Fetching metrics..."
	curl -s http://localhost:8080/metrics | head -20

# =============================================================================
# DOCKER
# =============================================================================

.PHONY: docker-build
docker-build: ## Build Docker image for backend
	@echo "Building Docker image..."
	docker build -f docker/Dockerfile.backend -t consonant-api:latest ./backend

.PHONY: docker-run
docker-run: docker-build ## Run backend in Docker
	@echo "Running backend in Docker..."
	docker-compose up -d api
	docker-compose logs -f api

# =============================================================================
# UTILITIES
# =============================================================================

.PHONY: build
build: backend-build ## Build all components
	@echo "All components built"

.PHONY: test
test: backend-test ## Run all tests
	@echo "All tests passed"

.PHONY: clean
clean: ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	rm -rf backend/bin
	rm -rf backend/coverage.out
	rm -rf backend/coverage.html
	@echo "Clean complete"

.PHONY: logs
logs: ## Follow all logs
	docker-compose logs -f

.PHONY: ps
ps: ## Show running services
	docker-compose ps

.PHONY: install-tools
install-tools: ## Install development tools
	@echo "Installing Go tools..."
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
	go install golang.org/x/tools/cmd/goimports@latest
	@echo "Tools installed. Make sure $(go env GOPATH)/bin is in your PATH"

# =============================================================================
# INITIALIZATION
# =============================================================================

.PHONY: init
init: install-tools infra-up backend-deps backend-generate backend-build ## Initialize development environment
	@echo ""
	@echo "==================================="
	@echo "Consonant development environment ready!"
	@echo "==================================="
	@echo ""
	@echo "Quick start:"
	@echo "  1. Run backend:    make backend-run"
	@echo "  2. Test API:       make test-balance"
	@echo "  3. View logs:      make logs"
	@echo "  4. Connect to DB:  make db-connect"
	@echo "  5. Connect to Redis: make redis-cli"
	@echo ""
	@echo "For help:          make help"
	@echo ""

# =============================================================================
# CI/CD
# =============================================================================

.PHONY: ci-test
ci-test: backend-deps backend-test backend-lint ## Run CI tests
	@echo "CI tests passed"

.PHONY: ci-build
ci-build: backend-deps backend-build docker-build ## Build for CI
	@echo "CI build complete"

# Default target
.DEFAULT_GOAL := help