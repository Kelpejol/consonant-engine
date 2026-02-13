# ⚡ Beam - Real-Time AI Cost Enforcement

<div align="center">

**Production-grade system for enforcing AI spending limits in real-time**

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)
[![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker)](https://hub.docker.com)

[Features](#-features) •
[Quick Start](#-quick-start) •
[Architecture](#-architecture) •
[API Reference](#-api-reference) •
[Contributing](#-contributing)

</div>

---

## 🎯 The Problem

B2B SaaS companies charge customers flat monthly fees ($500/month) but AI costs are completely **variable**. Some customers cost $5/month, others cost $500/month in AI expenses. Without real-time enforcement, you discover which customers are unprofitable **30 days later** when the bill arrives.

**Beam solves this by:**
- ⚡ **Pre-flight validation**: Check if customer can afford a request before it starts
- 🔄 **Streaming enforcement**: Count tokens and deduct balance in real-time during streaming
- 🛑 **Kill switch**: Immediately terminate streaming when balance hits zero
- 💯 **Perfect reconciliation**: Use provider's exact token counts for final billing

## ✨ Features

### Core Engine
- **Sub-5ms balance checks** via Redis Lua scripts
- **Atomic operations** prevent race conditions at scale
- **Dual storage**: Redis for speed, PostgreSQL for durability
- **Auto-reconciliation** between estimated and actual costs
- **Multi-provider support**: OpenAI, Anthropic, Google AI

### Production Ready
- **gRPC API** with Protocol Buffers for efficiency
- **REST API** for easy integration without gRPC clients
- **CLI tool** for manual operations and testing
- **Docker support** with docker-compose for instant setup
- **TimescaleDB** integration for time-series analytics
- **Prometheus metrics** for observability
- **Comprehensive logging** with structured JSON output

### Developer Experience
- **Standalone**: Works without SDK - direct API calls
- **Well documented** with examples and guides
- **Easy setup**: One command to run locally
- **Type-safe** Protocol Buffer definitions
- **Tested**: Unit and integration tests included

## 🚀 Quick Start

### Prerequisites
- Docker & Docker Compose
- Go 1.25+ (for building from source)
- Make (optional, for convenience)

### 1. Start Infrastructure

```bash
# Clone the repository
git clone https://github.com/kelpejol/beam
cd beam

# Start PostgreSQL and Redis
docker-compose up -d

# Wait for services to be ready (about 10 seconds)
docker-compose ps
```

### 2. Build and Run

```bash
# Build the binary
make build

# Run the server
./backend/bin/beam-api

# Or use Docker
docker-compose up -d beam-api
```

### 3. Test the API

```bash
# Using the CLI tool
./backend/bin/beam-cli balance get --customer-id test_customer_1

# Or with grpcurl
grpcurl -plaintext \
  -H "authorization: Bearer beam_test_key_1234567890" \
  -d '{"customer_id": "test_customer_1"}' \
  localhost:9090 beam.balance.v1.BalanceService/GetBalance

# Or with REST API
curl -H "Authorization: Bearer beam_test_key_1234567890" \
  http://localhost:8080/v1/balance/test_customer_1
```

**Response:**
```json
{
  "balance": "100000000",
  "reserved": "0",
  "available": "100000000"
}
```

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                     YOUR APPLICATION                             │
└───────────────────────┬─────────────────────────────────────────┘
                        │
                        │ gRPC/REST: CheckBalance()
                        ↓
┌─────────────────────────────────────────────────────────────────┐
│                   BEAM BACKEND (Go)                              │
│                                                                   │
│  ┌──────────────┐    ┌─────────────┐    ┌──────────────┐       │
│  │  gRPC/REST   │───→│   Ledger    │───→│ Redis (Hot)  │       │
│  │    API       │    │  (Atomic)   │    │  <1ms ops    │       │
│  └──────────────┘    └──────┬──────┘    └──────────────┘       │
│                              │                                    │
│                              ↓                                    │
│                      ┌──────────────┐                            │
│                      │  PostgreSQL  │                            │
│                      │  (Durable)   │                            │
│                      │ +TimescaleDB │                            │
│                      └──────────────┘                            │
└─────────────────────────────────────────────────────────────────┘
                        │
                        │ Forward to provider after approval
                        ↓
┌─────────────────────────────────────────────────────────────────┐
│              OpenAI / Anthropic / Google AI                      │
└─────────────────────────────────────────────────────────────────┘
```

### Key Components

**Ledger** - The heart of Beam
- Atomic balance operations using Redis Lua scripts
- Prevents race conditions with reservation system
- Automatic reconciliation of estimates vs actuals

**Storage Layer**
- **Redis**: Sub-millisecond balance checks, in-memory state
- **PostgreSQL**: Durable storage with complete audit trail
- **TimescaleDB**: Time-series optimizations for analytics

**API Layer**
- **gRPC**: High-performance binary protocol for production
- **REST**: HTTP/JSON for easy integration and testing
- **CLI**: Command-line tool for operations and debugging

## 📊 How It Works

### The Flow

1. **CheckBalance** - Pre-flight validation
   - Your app calls Beam before making an AI request
   - Beam checks if customer has enough balance
   - If yes, reserves grains and returns approval token
   - **Latency**: 2-4ms

2. **Make AI Request** - Your responsibility
   - Your app proceeds to call OpenAI/Anthropic/etc
   - Stream the response to your end user
   - Count tokens as they arrive

3. **DeductTokens** - Real-time deduction (optional but recommended)
   - Call Beam every ~50 tokens during streaming
   - Beam deducts from balance atomically
   - If balance hits zero, Beam returns `success: false` → **kill the stream**
   - **Latency**: 1-3ms per call

4. **FinalizeRequest** - Final reconciliation
   - Call once with exact token counts from provider
   - Beam reconciles estimated vs actual costs
   - Refunds overcharges, releases reservation
   - **Latency**: 3-8ms

## 🔌 API Reference

### REST API Endpoints

**Get Balance** - Query current balance
```bash
GET /v1/balance/:customer_id
Authorization: Bearer <api_key>

Response:
{
  "balance": "100000000",
  "reserved": "5000000",
  "available": "95000000"
}
```

**Check Balance** - Pre-flight validation
```bash
POST /v1/balance/check
Authorization: Bearer <api_key>
Content-Type: application/json

{
  "customer_id": "cus_123",
  "estimated_grains": 50000,
  "buffer_multiplier": 1.2,
  "request_id": "req_xyz",
  "metadata": {
    "model": "gpt-4",
    "max_tokens": 1000
  }
}

Response:
{
  "approved": true,
  "remaining_balance": "99950000",
  "request_token": "secure_token_xyz",
  "reserved_grains": 60000
}
```

**Deduct Tokens** - Real-time deduction
```bash
POST /v1/balance/deduct
Authorization: Bearer <api_key>
Content-Type: application/json

{
  "customer_id": "cus_123",
  "request_id": "req_xyz",
  "request_token": "secure_token_xyz",
  "tokens_consumed": 50,
  "model": "gpt-4",
  "is_completion": true
}

Response:
{
  "success": true,
  "remaining_balance": "99900000"
}
```

**Finalize Request** - Final reconciliation
```bash
POST /v1/balance/finalize
Authorization: Bearer <api_key>
Content-Type: application/json

{
  "customer_id": "cus_123",
  "request_id": "req_xyz",
  "status": "COMPLETED_SUCCESS",
  "actual_prompt_tokens": 234,
  "actual_completion_tokens": 487,
  "total_actual_cost_grains": 48700,
  "model": "gpt-4"
}

Response:
{
  "success": true,
  "refunded_grains": 11300,
  "final_balance": "99911300"
}
```

### gRPC API

Full Protocol Buffer definitions in [`proto/balance/v1/balance.proto`](proto/balance/v1/balance.proto)

```protobuf
service BalanceService {
  rpc CheckBalance(CheckBalanceRequest) returns (CheckBalanceResponse);
  rpc DeductTokens(DeductTokensRequest) returns (DeductTokensResponse);
  rpc FinalizeRequest(FinalizeRequestRequest) returns (FinalizeRequestResponse);
  rpc GetBalance(GetBalanceRequest) returns (GetBalanceResponse);
}
```

### CLI Tool

```bash
# Check balance
beam-cli balance get --customer-id cus_123

# Add balance (credit)
beam-cli balance add --customer-id cus_123 --amount 1000000 --description "Monthly top-up"

# Deduct balance (debit)
beam-cli balance deduct --customer-id cus_123 --amount 50000

# List recent requests
beam-cli requests list --customer-id cus_123 --limit 10

# Show request details
beam-cli requests show --request-id req_xyz

# Create new customer
beam-cli customers create --customer-id cus_new --name "New Customer" --balance 10000000

# Verify balance integrity
beam-cli admin verify-integrity --customer-id cus_123

# Sync Redis from PostgreSQL
beam-cli admin sync-all
```

## 💾 Database Schema

### Core Tables

**customers** - End customers with their balances
```sql
CREATE TABLE customers (
    customer_id VARCHAR(255) PRIMARY KEY,
    platform_user_id VARCHAR(255) NOT NULL,
    current_balance_grains BIGINT NOT NULL DEFAULT 0,
    lifetime_spent_grains BIGINT NOT NULL DEFAULT 0,
    buffer_strategy VARCHAR(20) DEFAULT 'conservative',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW(),
    CONSTRAINT positive_balance CHECK (current_balance_grains >= 0)
);
```

**transactions** - Append-only ledger (complete audit trail)
```sql
CREATE TABLE transactions (
    transaction_id VARCHAR(255) PRIMARY KEY,
    customer_id VARCHAR(255) NOT NULL,
    amount_grains BIGINT NOT NULL,  -- Positive=credit, Negative=debit
    transaction_type VARCHAR(50) NOT NULL,
    reference_id VARCHAR(255),
    description TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
```

**requests** - Detailed AI request tracking
```sql
CREATE TABLE requests (
    request_id VARCHAR(255) PRIMARY KEY,
    customer_id VARCHAR(255) NOT NULL,
    model VARCHAR(100) NOT NULL,
    estimated_cost_grains BIGINT NOT NULL,
    reserved_grains BIGINT NOT NULL,
    streaming_deducted_grains BIGINT DEFAULT 0,
    actual_cost_grains BIGINT,
    status VARCHAR(50) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP
);
```

## 🔐 Security

### API Authentication

Every request requires an API key:

```
Authorization: Bearer beam_sk_live_xxxxxxxxxxxxx
```

- Keys are hashed with SHA-256 before storage
- Stored in Redis for sub-millisecond authentication
- Plaintext keys never logged or stored

### Best Practices

- Use different API keys for development and production
- Rotate keys regularly
- Enable TLS in production
- Set appropriate rate limits
- Monitor for unusual activity

## 📈 Performance

### Latency Targets
- **CheckBalance**: < 5ms (typically 2-4ms)
- **DeductTokens**: < 3ms (typically 1-2ms)
- **FinalizeRequest**: < 10ms (typically 3-8ms)

### Throughput
- 10,000+ concurrent requests per server
- 100,000+ balance checks/second with horizontal scaling
- Sub-millisecond Redis operations via Lua scripts

### Scaling Strategies

**Horizontal Scaling**
```bash
# Run multiple instances
docker-compose up -d --scale beam-api=3

# Use load balancer (nginx, haproxy, etc)
# Configure health checks on /health endpoint
```

**Redis Scaling**
- Single Redis handles 100k+ operations/second
- If needed, shard by customer_id using Redis Cluster
- Use Redis Sentinel for high availability

**PostgreSQL Scaling**
- Read replicas for analytics queries
- Single primary for writes (sufficient for most use cases)
- Connection pooling prevents bottlenecks

## 🛠️ Development

### Project Structure

```
beam/
├── backend/
│   ├── cmd/
│   │   ├── api/              # Main server (gRPC + REST)
│   │   └── cli/              # CLI tool
│   ├── internal/
│   │   ├── api/              # gRPC service implementation
│   │   ├── rest/             # REST API handlers
│   │   ├── auth/             # API key authentication
│   │   ├── ledger/           # Core balance logic
│   │   └── sync/             # Redis-PostgreSQL sync
│   ├── pkg/proto/            # Generated protobuf code
│   └── migrations/           # Database migrations
├── scripts/
│   ├── lua/                  # Redis Lua scripts
│   └── load-test.js          # k6 load testing script
├── docs/                     # Detailed documentation
├── docker-compose.yml        # Local development environment
├── Dockerfile                # Production Docker image
└── Makefile                  # Build automation
```

### Building from Source

```bash
# Install dependencies
go mod download

# Generate protobuf code (requires protoc)
make proto

# Build all binaries
make build

# Binaries created at:
# - backend/bin/beam-api
# - backend/bin/beam-cli
```

### Running Tests

```bash
# Unit tests
make test

# Integration tests (requires Docker)
make test-integration

# Test coverage report
make test-coverage

# Benchmark tests
make benchmark
```

### Development Workflow

```bash
# 1. Start infrastructure
docker-compose up -d postgres redis

# 2. Run server in dev mode (with auto-reload)
make dev

# 3. In another terminal, test the API
./backend/bin/beam-cli balance get --customer-id test_customer_1

# 4. View logs
docker-compose logs -f beam-api

# 5. Clean up
make clean
```

## 🧪 Testing

### Manual API Testing

Complete example workflow:

```bash
# 1. Check initial balance
curl -H "Authorization: Bearer beam_test_key_1234567890" \
  http://localhost:8080/v1/balance/test_customer_1

# 2. Pre-flight check (reserve grains)
curl -X POST -H "Authorization: Bearer beam_test_key_1234567890" \
  -H "Content-Type: application/json" \
  -d '{
    "customer_id": "test_customer_1",
    "estimated_grains": 50000,
    "buffer_multiplier": 1.2,
    "request_id": "req_test_'$(date +%s)'",
    "metadata": {"model": "gpt-4"}
  }' \
  http://localhost:8080/v1/balance/check

# Save the request_token from response, then:

# 3. Simulate streaming deductions (repeat as needed)
curl -X POST -H "Authorization: Bearer beam_test_key_1234567890" \
  -H "Content-Type: application/json" \
  -d '{
    "customer_id": "test_customer_1",
    "request_id": "req_test_'$(date +%s)'",
    "request_token": "YOUR_TOKEN_HERE",
    "tokens_consumed": 50,
    "model": "gpt-4",
    "is_completion": true
  }' \
  http://localhost:8080/v1/balance/deduct

# 4. Finalize with exact costs
curl -X POST -H "Authorization: Bearer beam_test_key_1234567890" \
  -H "Content-Type: application/json" \
  -d '{
    "customer_id": "test_customer_1",
    "request_id": "req_test_'$(date +%s)'",
    "status": "COMPLETED_SUCCESS",
    "actual_prompt_tokens": 234,
    "actual_completion_tokens": 487,
    "total_actual_cost_grains": 48700,
    "model": "gpt-4"
  }' \
  http://localhost:8080/v1/balance/finalize

# 5. Verify final balance
curl -H "Authorization: Bearer beam_test_key_1234567890" \
  http://localhost:8080/v1/balance/test_customer_1
```

### Load Testing

```bash
# Install k6
brew install k6  # macOS
# or: https://k6.io/docs/getting-started/installation

# Run load test
k6 run scripts/load-test.js

# Custom scenario
k6 run --vus 100 --duration 30s scripts/load-test.js
```

## 📚 Documentation

Comprehensive guides available in [`docs/`](docs/):

- [**API Reference**](docs/API.md) - Complete API documentation with examples
- [**Architecture Deep Dive**](docs/ARCHITECTURE.md) - System design and decisions
- [**Integration Guide**](docs/INTEGRATION.md) - How to integrate Beam into your app
- [**Operations Guide**](docs/OPERATIONS.md) - Production deployment and monitoring
- [**Performance Tuning**](docs/PERFORMANCE.md) - Optimization strategies
- [**Database Guide**](docs/DATABASE.md) - Schema details and queries

## 🤝 Contributing

We love contributions! See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Quick Contribution Guide

1. **Fork** the repository
2. **Create** a feature branch (`git checkout -b feature/amazing-feature`)
3. **Make** your changes
4. **Test** thoroughly (`make test`)
5. **Commit** (`git commit -m 'Add amazing feature'`)
6. **Push** (`git push origin feature/amazing-feature`)
7. **Open** a Pull Request

### Development Standards

- Follow [Effective Go](https://golang.org/doc/effective_go) guidelines
- Write tests for new features (maintain >80% coverage)
- Update documentation for API changes
- Use conventional commit messages
- Add examples for new features

## 🐛 Known Issues & Roadmap

### Known Issues
- None! (Please report any issues you find)

### Roadmap

**v1.0** (Current)
- ✅ Core balance operations
- ✅ gRPC and REST APIs
- ✅ CLI tool
- ✅ Docker support
- ✅ TimescaleDB integration

**v1.1** (Planned)
- [ ] WebSocket API for real-time balance updates
- [ ] GraphQL API
- [ ] Multi-region deployment support
- [ ] Advanced analytics dashboard

**v2.0** (Future)
- [ ] Multi-currency support
- [ ] Automatic cost optimization recommendations
- [ ] Machine learning for cost prediction
- [ ] Stripe/payment provider integrations

## 📄 License

This project is licensed under the **MIT License** - see the [LICENSE](LICENSE) file for details.

### What this means:
- ✅ Commercial use allowed
- ✅ Modification allowed
- ✅ Distribution allowed
- ✅ Private use allowed
- ⚠️ No warranty provided
- ⚠️ No liability

## 🙏 Acknowledgments

Built with amazing open source tools:

- [**Go**](https://golang.org/) - Efficient, concurrent backend language
- [**gRPC**](https://grpc.io/) - High-performance RPC framework
- [**Protocol Buffers**](https://developers.google.com/protocol-buffers) - Type-safe serialization
- [**Redis**](https://redis.io/) - Lightning-fast in-memory database
- [**PostgreSQL**](https://www.postgresql.org/) - Rock-solid relational database
- [**TimescaleDB**](https://www.timescale.com/) - Time-series superpowers for PostgreSQL
- [**zerolog**](https://github.com/rs/zerolog) - Zero-allocation structured logging

Special thanks to all contributors and users!

## 💬 Community & Support

- **GitHub Issues**: [Report bugs](https://github.com/kelpejol/beam/issues)
- **GitHub Discussions**: [Ask questions](https://github.com/kelpejol/beam/discussions)
- **Documentation**: [Read the docs](docs/)
- **Examples**: [See examples](examples/)

## ⭐ Star Us!

If you find Beam useful, please consider giving it a star on GitHub! It helps others discover the project.

---

<div align="center">

**Made with ⚡ by developers, for developers**

[Documentation](docs/) • [Contributing](CONTRIBUTING.md) • [License](LICENSE) • [Changelog](CHANGELOG.md)

</div>