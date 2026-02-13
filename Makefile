# Makefile for Beam - Real-Time AI Cost Enforcement
#
# This Makefile provides convenient targets for building, testing, and running
# the Beam system during development and deployment.
#
# Common commands:
#   make help             - Show all available targets
#   make dev              - Start full development environment
#   make build            - Build all binaries
#   make test             - Run all tests
#   make clean            - Clean build artifacts

.PHONY: help
.DEFAULT_GOAL := help

# Colors for output
BLUE := \033[34m
GREEN := \033[32m
YELLOW := \033[33m
RED := \033[31m
RESET := \033[0m

# Build variables
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
GO_VERSION := 1.25
LDFLAGS := -X main.Version=$(VERSION) -X main.BuildTime=$(BUILD_TIME)

# Directories
ROOT_DIR := $(shell pwd)
BACKEND_DIR := $(ROOT_DIR)/backend
CMD_DIR := $(BACKEND_DIR)/cmd
BIN_DIR := $(BACKEND_DIR)/bin
PROTO_DIR := $(ROOT_DIR)/proto
SCRIPTS_DIR := $(ROOT_DIR)/scripts

help: ## Show this help message
	@echo '$(BLUE)Beam - Real-Time AI Cost Enforcement$(RESET)'
	@echo ''
	@echo '$(GREEN)Usage:$(RESET)'
	@echo '  make $(YELLOW)<target>$(RESET)'
	@echo ''
	@echo '$(GREEN)Available targets:$(RESET)'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  $(YELLOW)%-20s$(RESET) %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# =============================================================================
# DEVELOPMENT
# =============================================================================

.PHONY: dev
dev: infra-up build ## Start full development environment
	@echo "$(GREEN)Starting Beam API in development mode...$(RESET)"
	@cd $(BACKEND_DIR) && \
		GRPC_PORT=9090 \
		HTTP_PORT=8080 \
		REDIS_ADDR=localhost:6379 \
		POSTGRES_URL="postgres://postgres:postgres@localhost:5432/beam?sslmode=disable" \
		LOG_LEVEL=debug \
		ENVIRONMENT=development \
		./bin/beam-api

.PHONY: infra-up
infra-up: ## Start PostgreSQL and Redis
	@echo "$(BLUE)Starting infrastructure...$(RESET)"
	@docker-compose up -d postgres redis
	@echo "$(GREEN)Waiting for services to be healthy...$(RESET)"
	@sleep 5
	@docker-compose ps
	@echo "$(GREEN)Infrastructure ready!$(RESET)"

.PHONY: infra-down
infra-down: ## Stop infrastructure
	@echo "$(YELLOW)Stopping infrastructure...$(RESET)"
	@docker-compose down

.PHONY: infra-clean
infra-clean: ## Stop infrastructure and remove volumes (fresh start)
	@echo "$(RED)Cleaning infrastructure (this will delete all data)...$(RESET)"
	@docker-compose down -v
	@echo "$(GREEN)All data removed. Run 'make infra-up' for fresh start.$(RESET)"

.PHONY: infra-logs
infra-logs: ## Follow infrastructure logs
	@docker-compose logs -f postgres redis beam-api

# =============================================================================
# BUILD
# =============================================================================

.PHONY: build
build: build-api build-cli ## Build all binaries

.PHONY: build-api
build-api: ## Build API server
	@echo "$(BLUE)Building Beam API...$(RESET)"
	@mkdir -p $(BIN_DIR)
	@cd $(BACKEND_DIR) && go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/beam-api ./cmd/api
	@echo "$(GREEN)✓ Built: $(BIN_DIR)/beam-api$(RESET)"

.PHONY: build-cli
build-cli: ## Build CLI tool
	@echo "$(BLUE)Building Beam CLI...$(RESET)"
	@mkdir -p $(BIN_DIR)
	@cd $(BACKEND_DIR) && go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/beam-cli ./cmd/cli
	@echo "$(GREEN)✓ Built: $(BIN_DIR)/beam-cli$(RESET)"

.PHONY: install
install: build ## Install binaries to $GOPATH/bin
	@echo "$(BLUE)Installing binaries...$(RESET)"
	@cp $(BIN_DIR)/beam-api $(GOPATH)/bin/
	@cp $(BIN_DIR)/beam-cli $(GOPATH)/bin/
	@echo "$(GREEN)✓ Installed to $(GOPATH)/bin$(RESET)"

# =============================================================================
# DEPENDENCIES
# =============================================================================

.PHONY: deps
deps: ## Download Go dependencies
	@echo "$(BLUE)Downloading Go dependencies...$(RESET)"
	@cd $(BACKEND_DIR) && go mod download
	@cd $(BACKEND_DIR) && go mod tidy
	@echo "$(GREEN)✓ Dependencies downloaded$(RESET)"

.PHONY: deps-update
deps-update: ## Update Go dependencies
	@echo "$(BLUE)Updating Go dependencies...$(RESET)"
	@cd $(BACKEND_DIR) && go get -u ./...
	@cd $(BACKEND_DIR) && go mod tidy
	@echo "$(GREEN)✓ Dependencies updated$(RESET)"

# =============================================================================
# PROTOBUF
# =============================================================================

.PHONY: proto
proto: ## Generate protobuf code
	@echo "$(BLUE)Generating protobuf code...$(RESET)"
	@cd $(BACKEND_DIR) && protoc \
		--go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		../proto/balance/v1/balance.proto
	@echo "$(GREEN)✓ Protobuf code generated$(RESET)"

.PHONY: proto-check
proto-check: ## Check if protobuf code is up to date
	@echo "$(BLUE)Checking protobuf code...$(RESET)"
	@cd $(BACKEND_DIR) && buf breaking --against '.git#branch=main'

# =============================================================================
# TESTING
# =============================================================================

.PHONY: test
test: ## Run all tests
	@echo "$(BLUE)Running tests...$(RESET)"
	@cd $(BACKEND_DIR) && go test -v -race ./...
	@echo "$(GREEN)✓ All tests passed$(RESET)"

.PHONY: test-unit
test-unit: ## Run unit tests only
	@echo "$(BLUE)Running unit tests...$(RESET)"
	@cd $(BACKEND_DIR) && go test -v -race -short ./...

.PHONY: test-integration
test-integration: infra-up ## Run integration tests
	@echo "$(BLUE)Running integration tests...$(RESET)"
	@cd $(BACKEND_DIR) && go test -v -race -run Integration ./...

.PHONY: test-coverage
test-coverage: ## Run tests with coverage report
	@echo "$(BLUE)Running tests with coverage...$(RESET)"
	@cd $(BACKEND_DIR) && go test -v -race -coverprofile=coverage.out ./...
	@cd $(BACKEND_DIR) && go tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)✓ Coverage report: $(BACKEND_DIR)/coverage.html$(RESET)"
	@cd $(BACKEND_DIR) && go tool cover -func=coverage.out | grep total

.PHONY: benchmark
benchmark: ## Run benchmark tests
	@echo "$(BLUE)Running benchmarks...$(RESET)"
	@cd $(BACKEND_DIR) && go test -bench=. -benchmem -run=^# ./...

# =============================================================================
# CODE QUALITY
# =============================================================================

.PHONY: lint
lint: ## Run linters
	@echo "$(BLUE)Running linters...$(RESET)"
	@cd $(BACKEND_DIR) && go vet ./...
	@cd $(BACKEND_DIR) && golangci-lint run ./...
	@echo "$(GREEN)✓ Linting passed$(RESET)"

.PHONY: format
format: ## Format code
	@echo "$(BLUE)Formatting code...$(RESET)"
	@cd $(BACKEND_DIR) && go fmt ./...
	@cd $(BACKEND_DIR) && goimports -w .
	@echo "$(GREEN)✓ Code formatted$(RESET)"

.PHONY: check
check: format lint test ## Run all checks (format, lint, test)
	@echo "$(GREEN)✓ All checks passed$(RESET)"

# =============================================================================
# DATABASE
# =============================================================================

.PHONY: db-connect
db-connect: ## Connect to PostgreSQL
	@docker-compose exec postgres psql -U postgres -d beam

.PHONY: db-migrate
db-migrate: ## Run database migrations
	@echo "$(BLUE)Running migrations...$(RESET)"
	@docker-compose exec postgres psql -U postgres -d beam -f /docker-entrypoint-initdb.d/001_initial_schema.up.sql
	@echo "$(GREEN)✓ Migrations complete$(RESET)"

.PHONY: db-reset
db-reset: infra-clean infra-up ## Reset database to fresh state
	@echo "$(GREEN)✓ Database reset complete$(RESET)"

.PHONY: db-check
db-check: ## Check database integrity
	@echo "$(BLUE)Checking database integrity...$(RESET)"
	@docker-compose exec postgres psql -U postgres -d beam -c "SELECT * FROM verify_balance_integrity('test_customer_1');"

.PHONY: db-seed
db-seed: ## Seed database with test data
	@echo "$(BLUE)Seeding database...$(RESET)"
	@docker-compose exec -T postgres psql -U postgres -d beam < test_seed.sql
	@echo "$(GREEN)✓ Database seeded$(RESET)"

# =============================================================================
# REDIS
# =============================================================================

.PHONY: redis-cli
redis-cli: ## Connect to Redis CLI
	@docker-compose exec redis redis-cli

.PHONY: redis-monitor
redis-monitor: ## Monitor Redis commands in real-time
	@docker-compose exec redis redis-cli MONITOR

.PHONY: redis-stats
redis-stats: ## Show Redis statistics
	@docker-compose exec redis redis-cli INFO stats

.PHONY: redis-flush
redis-flush: ## Flush all Redis data (DANGEROUS)
	@echo "$(RED)WARNING: This will delete all Redis data!$(RESET)"
	@read -p "Are you sure? [y/N] " -n 1 -r; \
	echo; \
	if [[ $$REPLY =~ ^[Yy]$$ ]]; then \
		docker-compose exec redis redis-cli FLUSHALL; \
		echo "$(GREEN)✓ Redis flushed$(RESET)"; \
	fi

# =============================================================================
# DOCKER
# =============================================================================

.PHONY: docker-build
docker-build: ## Build Docker image
	@echo "$(BLUE)Building Docker image...$(RESET)"
	@docker build -f docker/Dockerfile -t beam-api:$(VERSION) ./backend
	@docker tag beam-api:$(VERSION) beam-api:latest
	@echo "$(GREEN)✓ Docker image built: beam-api:$(VERSION)$(RESET)"

.PHONY: docker-run
docker-run: docker-build ## Run in Docker
	@echo "$(BLUE)Running in Docker...$(RESET)"
	@docker-compose up -d
	@docker-compose logs -f beam-api

.PHONY: docker-push
docker-push: docker-build ## Push Docker image to registry
	@echo "$(BLUE)Pushing Docker image...$(RESET)"
	@docker push beam-api:$(VERSION)
	@docker push beam-api:latest

# =============================================================================
# API TESTING
# =============================================================================

.PHONY: test-api
test-api: ## Test API with sample requests
	@echo "$(BLUE)Testing API endpoints...$(RESET)"
	@echo "$(YELLOW)1. Get Balance$(RESET)"
	@curl -s -H "Authorization: Bearer beam_test_key_1234567890" \
		http://localhost:8080/v1/balance/test_customer_1 | jq .
	@echo ""
	@echo "$(YELLOW)2. Check Balance$(RESET)"
	@curl -s -X POST -H "Authorization: Bearer beam_test_key_1234567890" \
		-H "Content-Type: application/json" \
		-d '{"customer_id":"test_customer_1","estimated_grains":50000,"request_id":"req_test_'$(shell date +%s)'","buffer_multiplier":1.2}' \
		http://localhost:8080/v1/balance/check | jq .
	@echo "$(GREEN)✓ API tests complete$(RESET)"

.PHONY: test-grpc
test-grpc: ## Test gRPC API with grpcurl
	@echo "$(BLUE)Testing gRPC API...$(RESET)"
	@grpcurl -plaintext \
		-H "authorization: Bearer beam_test_key_1234567890" \
		-d '{"customer_id": "test_customer_1"}' \
		localhost:9090 beam.balance.v1.BalanceService/GetBalance
	@echo "$(GREEN)✓ gRPC test complete$(RESET)"

.PHONY: test-health
test-health: ## Test health endpoints
	@echo "$(BLUE)Testing health endpoints...$(RESET)"
	@echo "$(YELLOW)Health:$(RESET)"
	@curl -s http://localhost:8080/health
	@echo ""
	@echo "$(YELLOW)Ready:$(RESET)"
	@curl -s http://localhost:8080/ready
	@echo ""
	@echo "$(GREEN)✓ Health checks passed$(RESET)"

.PHONY: test-metrics
test-metrics: ## View Prometheus metrics
	@echo "$(BLUE)Fetching metrics...$(RESET)"
	@curl -s http://localhost:8080/metrics | head -30

.PHONY: load-test
load-test: ## Run load test with k6
	@echo "$(BLUE)Running load test...$(RESET)"
	@k6 run $(SCRIPTS_DIR)/load-test.js

# =============================================================================
# CLI COMMANDS
# =============================================================================

.PHONY: cli-balance
cli-balance: ## Get balance using CLI
	@$(BIN_DIR)/beam-cli balance get --customer-id test_customer_1

.PHONY: cli-customers
cli-customers: ## List customers using CLI
	@$(BIN_DIR)/beam-cli customers list

.PHONY: cli-requests
cli-requests: ## List requests using CLI
	@$(BIN_DIR)/beam-cli requests list --customer-id test_customer_1 --limit 10

# =============================================================================
# UTILITIES
# =============================================================================

.PHONY: clean
clean: ## Clean build artifacts
	@echo "$(BLUE)Cleaning build artifacts...$(RESET)"
	@rm -rf $(BIN_DIR)
	@rm -rf $(BACKEND_DIR)/coverage.out
	@rm -rf $(BACKEND_DIR)/coverage.html
	@find $(BACKEND_DIR) -name "*.test" -type f -delete
	@echo "$(GREEN)✓ Clean complete$(RESET)"

.PHONY: logs
logs: ## Follow all logs
	@docker-compose logs -f

.PHONY: ps
ps: ## Show running services
	@docker-compose ps

.PHONY: install-tools
install-tools: ## Install development tools
	@echo "$(BLUE)Installing development tools...$(RESET)"
	@go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	@go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	@go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
	@go install golang.org/x/tools/cmd/goimports@latest
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo "$(GREEN)✓ Tools installed$(RESET)"
	@echo "$(YELLOW)Make sure $(shell go env GOPATH)/bin is in your PATH$(RESET)"

# =============================================================================
# INITIALIZATION
# =============================================================================

.PHONY: init
init: install-tools infra-up deps proto build ## Initialize complete development environment
	@echo ""
	@echo "$(GREEN)========================================$(RESET)"
	@echo "$(GREEN)Beam development environment ready!$(RESET)"
	@echo "$(GREEN)========================================$(RESET)"
	@echo ""
	@echo "$(BLUE)Quick start:$(RESET)"
	@echo "  1. Run server:        $(YELLOW)make dev$(RESET)"
	@echo "  2. Test API:          $(YELLOW)make test-api$(RESET)"
	@echo "  3. Run tests:         $(YELLOW)make test$(RESET)"
	@echo "  4. View logs:         $(YELLOW)make logs$(RESET)"
	@echo "  5. Connect to DB:     $(YELLOW)make db-connect$(RESET)"
	@echo "  6. Connect to Redis:  $(YELLOW)make redis-cli$(RESET)"
	@echo ""
	@echo "$(BLUE)For help:$(RESET) $(YELLOW)make help$(RESET)"
	@echo ""

# =============================================================================
# CI/CD
# =============================================================================

.PHONY: ci-test
ci-test: deps proto lint test ## Run CI tests
	@echo "$(GREEN)✓ CI tests passed$(RESET)"

.PHONY: ci-build
ci-build: deps proto build docker-build ## Build for CI
	@echo "$(GREEN)✓ CI build complete$(RESET)"

# =============================================================================
# RELEASE
# =============================================================================

.PHONY: release-check
release-check: check test-coverage ## Pre-release checks
	@echo "$(GREEN)✓ Release checks passed$(RESET)"

.PHONY: release-notes
release-notes: ## Generate release notes
	@echo "$(BLUE)Generating release notes...$(RESET)"
	@git log $(shell git describe --tags --abbrev=0)..HEAD --pretty=format:"- %s (%h)" --no-merges

.PHONY: version
version: ## Show current version
	@echo "$(BLUE)Current version:$(RESET) $(VERSION)"
	@echo "$(BLUE)Go version:$(RESET) $(GO_VERSION)"
	@echo "$(BLUE)Build time:$(RESET) $(BUILD_TIME)"