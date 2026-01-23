# Consonant - Real-Time AI Cost Enforcement System

Consonant is a production-grade system for enforcing AI spending limits in real-time. It sits between your application and AI providers (OpenAI, Anthropic, Google) to prevent customers from exceeding their allocated budgets **during streaming**, not after the bill arrives.

## 🎯 The Problem We Solve

B2B SaaS companies charge customers flat monthly fees (like $500/month) but AI costs are completely variable. Some customers cost $5/month, others cost $500/month in AI expenses. Without real-time enforcement, you discover which customers are unprofitable **30 days later** when the bill arrives.

Consonant solves this by:
- **Pre-flight validation**: Check if customer can afford a request before it starts
- **Streaming enforcement**: Count tokens and deduct grains in real-time as response streams
- **Kill switch**: Immediately terminate streaming when balance hits zero
- **Perfect reconciliation**: Use provider's exact token counts for final billing

## 🏗️ Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                      YOUR APPLICATION                            │
│  (wrapped with Consonant SDK)                                    │
└─────────────────┬───────────────────────────────────────────────┘
                  │
                  │ 1. Pre-flight check (gRPC)
                  ↓
┌─────────────────────────────────────────────────────────────────┐
│              CONSONANT BACKEND (This Repository)                 │
│                                                                   │
│  ┌──────────────┐    ┌─────────────┐    ┌──────────────┐       │
│  │   gRPC API   │───→│   Ledger    │───→│  Redis (Hot) │       │
│  │   (Go 1.25)  │    │  (Atomic)   │    │  <1ms ops    │       │
│  └──────────────┘    └──────┬──────┘    └──────────────┘       │
│                              │                                    │
│                              ↓                                    │
│                      ┌──────────────┐                            │
│                      │  PostgreSQL  │                            │
│                      │ (Durable)    │                            │
│                      │ +TimescaleDB │                            │
│                      └──────────────┘                            │
└─────────────────────────────────────────────────────────────────┘
                  │
                  │ 2. Request approved, forward to provider
                  ↓
┌─────────────────────────────────────────────────────────────────┐
│              OpenAI / Anthropic / Google                         │
│              (AI Provider - streaming response)                  │
└─────────────────────────────────────────────────────────────────┘
                  │
                  │ 3. Response streams back through SDK
                  ↓
┌─────────────────────────────────────────────────────────────────┐
│                    CONSONANT SDK                                 │
│  • Counts tokens in each chunk                                   │
│  • Batches deductions (every 50 tokens)                         │
│  • Calls backend to deduct grains                               │
│  • Kills stream if balance hits zero                            │
└─────────────────────────────────────────────────────────────────┘
```

### Key Components

**Backend (Go)**
- **Ledger**: Atomic balance operations using Redis Lua scripts
- **gRPC API**: Sub-5ms latency balance checks
- **PostgreSQL**: Durable storage with complete audit trail
- **TimescaleDB**: Time-series optimizations for requests table

**SDK (TypeScript/Python)**
- **Client Wrapper**: Transparent interception of AI client calls
- **Streaming Interceptor**: Real-time token counting during streaming
- **Batched Deductions**: Minimize backend traffic (deduct every 50 tokens)
- **Kill Switch**: Immediate stream termination when balance exhausted

## 🚀 Quick Start

### Prerequisites

- Docker and Docker Compose (for infrastructure)
- Go 1.25+ (for backend)
- Node.js 18+ (for TypeScript SDK)
- Python 3.10+ (for Python SDK)

### 1. Start Infrastructure

```bash
# Clone repository
git clone https://github.com/your-org/consonant-system
cd consonant-system

# Start PostgreSQL and Redis
docker-compose up -d postgres redis

# Verify services are healthy
docker-compose ps

# Check logs
docker-compose logs -f postgres redis
```

The database migrations run automatically when PostgreSQL starts. You'll see TimescaleDB tables created with sample data.

### 2. Build and Run Backend

```bash
cd backend

# Download dependencies
go mod download

# Generate protobuf code (requires protoc and protoc-gen-go-grpc)
# protoc --go_out=. --go-grpc_out=. proto/balance/v1/balance.proto

# Build the server
go build -o bin/api ./cmd/api

# Run the server
./bin/api

# You should see:
# {"level":"info","time":1706234567,"message":"starting consonant api server"}
# {"level":"info","time":1706234567,"message":"connected to redis"}
# {"level":"info","time":1706234567,"message":"ledger initialized"}
# {"level":"info","time":1706234567,"message":"grpc server listening","port":"9090"}
# {"level":"info","time":1706234567,"message":"http server listening","port":"8080"}
```

### 3. Test the API

```bash
# Install grpcurl if you don't have it
# go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

# Check balance for test customer
grpcurl -plaintext \
  -H "authorization: Bearer consonant_test_key_1234567890" \
  -d '{"customer_id": "test_customer_1"}' \
  localhost:9090 consonant.balance.v1.BalanceService/GetBalance

# Expected response:
# {
#   "balance": "100000000",
#   "reserved": "0",
#   "available": "100000000"
# }
```

### 4. Integrate SDK (Coming Soon)

The TypeScript and Python SDKs will be in `sdk-typescript/` and `sdk-python/` directories. They wrap your existing OpenAI/Anthropic clients transparently.

Example usage (TypeScript):
```typescript
import OpenAI from 'openai';
import { Consonant } from '@consonant/sdk';

const openai = new OpenAI({ apiKey: process.env.OPENAI_API_KEY });

// Wrap the client with Consonant
const consonant = new Consonant({
  apiKey: process.env.CONSONANT_API_KEY,
  customerIdExtractor: (ctx) => ctx.userId
});

const protectedClient = consonant.wrap(openai);

// Use exactly the same API - protection is transparent
const response = await protectedClient.chat.completions.create({
  model: 'gpt-4',
  messages: [{ role: 'user', content: 'Hello!' }],
  stream: true
}, { context: { userId: 'user_123' } });

// If customer runs out of balance, SDK throws InsufficientBalanceError
```

## 📊 Database Schema

### Customers Table
Stores end customers with their current grain balance.

```sql
CREATE TABLE customers (
    customer_id VARCHAR(255) PRIMARY KEY,
    platform_user_id VARCHAR(255) NOT NULL,
    name VARCHAR(500),
    current_balance_grains BIGINT NOT NULL DEFAULT 0,
    lifetime_spent_grains BIGINT NOT NULL DEFAULT 0,
    buffer_strategy VARCHAR(20) DEFAULT 'conservative',
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
```

### Transactions Table (TimescaleDB Hypertable)
Append-only ledger of all grain movements.

```sql
CREATE TABLE transactions (
    transaction_id VARCHAR(255) PRIMARY KEY,
    customer_id VARCHAR(255) NOT NULL,
    amount_grains BIGINT NOT NULL,
    transaction_type VARCHAR(50) NOT NULL,
    reference_id VARCHAR(255),
    description TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

SELECT create_hypertable('transactions', 'created_at');
```

### Requests Table (TimescaleDB Hypertable)
Detailed record of every AI request for analytics.

```sql
CREATE TABLE requests (
    request_id VARCHAR(255) PRIMARY KEY,
    customer_id VARCHAR(255) NOT NULL,
    model VARCHAR(100) NOT NULL,
    estimated_cost_grains BIGINT NOT NULL,
    reserved_grains BIGINT NOT NULL,
    streaming_deducted_grains BIGINT DEFAULT 0,
    provider_reported_cost_grains BIGINT,
    actual_cost_grains BIGINT,
    status VARCHAR(50) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP
);

SELECT create_hypertable('requests', 'created_at');
```

## 🔐 Security

### API Authentication
Every request must include an API key in the authorization header:

```
authorization: Bearer consonant_sk_live_xxxxx
```

API keys are hashed with SHA-256 and stored in Redis for fast lookup. The plaintext key is never stored.

### Data Protection
- All communication over TLS in production
- API keys never logged or exposed
- Customer data isolated by platform_user_id
- Database connections use SSL in production

## 📈 Performance

### Latency Targets
- CheckBalance: < 5ms (typically 2-4ms)
- DeductTokens: < 3ms (typically 1-2ms)
- FinalizeRequest: < 10ms (typically 3-8ms)

### Throughput
- 10,000+ concurrent requests per server
- 100,000+ balance checks per second with horizontal scaling
- Sub-millisecond Redis operations via Lua scripts

### Scaling
- **Horizontal**: Add more backend instances behind load balancer
- **Redis**: Shard by customer_id if needed (unlikely until 100k+ customers)
- **PostgreSQL**: Read replicas for analytics, single primary for writes

## 🧪 Testing

```bash
cd backend

# Run all tests
go test ./...

# Run with race detector
go test -race ./...

# Run specific package
go test ./internal/ledger/...

# Run with coverage
go test -cover ./...
```

## 🐛 Debugging

### Check Redis State

```bash
# Connect to Redis
docker-compose exec redis redis-cli

# Check customer balance
GET customer:balance:test_customer_1

# Check reserved grains
GET customer:reserved:test_customer_1

# List all request tracking hashes
SCAN 0 MATCH request:*

# Inspect specific request
HGETALL request:req_xyz123
```

### Check PostgreSQL State

```bash
# Connect to database
docker-compose exec postgres psql -U postgres -d consonant

# Check customer balances
SELECT customer_id, name, current_balance_grains, 
       current_balance_grains / 1000000 AS balance_dollars
FROM customers;

# Check recent transactions
SELECT * FROM transactions 
ORDER BY created_at DESC 
LIMIT 10;

# Check recent requests
SELECT request_id, customer_id, model, status,
       actual_cost_grains / 1000000 AS cost_dollars
FROM requests 
ORDER BY created_at DESC 
LIMIT 10;

# Verify balance integrity for a customer
SELECT * FROM verify_balance_integrity('test_customer_1');
```

### View Logs

```bash
# Backend logs (if running directly)
./bin/api

# Backend logs (if running via docker-compose)
docker-compose logs -f api

# Filter for specific customer
docker-compose logs api | grep "customer_id=test_customer_1"

# Filter for errors
docker-compose logs api | grep "level=error"
```

## 🚨 Common Issues

### "Failed to connect to redis"
- Ensure Redis is running: `docker-compose ps redis`
- Check Redis is healthy: `docker-compose exec redis redis-cli ping`
- Verify Redis port: `netstat -an | grep 6379`

### "Failed to connect to postgres"
- Ensure PostgreSQL is running: `docker-compose ps postgres`
- Check PostgreSQL is healthy: `docker-compose exec postgres pg_isready`
- Verify credentials match docker-compose.yml

### "Invalid API key"
- API key not stored in Redis
- For development, the test key `consonant_test_key_1234567890` is auto-stored
- Check Redis: `docker-compose exec redis redis-cli GET apikey:<hash>`

### "Request not found" during deduction
- Request tracking hash expired (TTL = 1 hour)
- Usually means request took too long or SDK crashed mid-stream
- Check request status in PostgreSQL: `SELECT * FROM requests WHERE request_id = '...'`

## 📝 Production Deployment

### Environment Variables

```bash
# Required
export GRPC_PORT=9090
export HTTP_PORT=8080
export REDIS_ADDR=redis.production.example.com:6379
export POSTGRES_URL=postgres://user:pass@postgres.example.com:5432/consonant?sslmode=require
export ENVIRONMENT=production

# Optional
export LOG_LEVEL=info
export GRPC_MAX_RECV_MSG_SIZE=4194304
export GRPC_MAX_SEND_MSG_SIZE=4194304
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: consonant-api
spec:
  replicas: 3
  selector:
    matchLabels:
      app: consonant-api
  template:
    metadata:
      labels:
        app: consonant-api
    spec:
      containers:
      - name: api
        image: your-registry/consonant-api:latest
        ports:
        - containerPort: 9090
          name: grpc
        - containerPort: 8080
          name: http
        env:
        - name: REDIS_ADDR
          value: "redis-service:6379"
        - name: POSTGRES_URL
          valueFrom:
            secretKeyRef:
              name: consonant-secrets
              key: postgres-url
        livenessProbe:
          httpGet:
            path: /health
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /ready
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
```

## 🔧 Configuration

### Buffer Strategy
Controls how conservative pre-flight reservations are:

- **Conservative (1.2x)**: Reserve 20% more than estimated
  - Safer but may reject requests unnecessarily
  - Recommended for production
  
- **Aggressive (1.0x)**: Reserve exact estimate
  - Maximizes utilization
  - Slightly higher risk of overruns

### Grain Conversion
1 million grains = $1 by default. This gives 6 decimal places of precision for accurate tracking of fractional-cent costs.

Example:
- Customer pays $500/month
- You allocate 50% to AI costs = $250
- Customer gets 250,000,000 grains

## 📚 Additional Documentation

- [Architecture Deep Dive](docs/architecture.md) - Complete system design
- [API Reference](docs/api.md) - gRPC method documentation  
- [SDK Usage](docs/sdk.md) - Integration examples
- [Ops Runbook](docs/ops.md) - Operational procedures
- [Performance Tuning](docs/performance.md) - Optimization guide

## 🤝 Contributing

Contributions are welcome! Please read our [Contributing Guide](CONTRIBUTING.md) first.

## 📄 License

MIT License - see [LICENSE](LICENSE) for details.

## 🙏 Acknowledgments

Built with:
- [Go](https://golang.org/) - Backend implementation
- [gRPC](https://grpc.io/) - High-performance RPC framework
- [Redis](https://redis.io/) - In-memory data structure store
- [PostgreSQL](https://www.postgresql.org/) - Relational database
- [TimescaleDB](https://www.timescale.com/) - Time-series database extension
- [zerolog](https://github.com/rs/zerolog) - Structured logging

## 📞 Support

- Issues: [GitHub Issues](https://github.com/your-org/consonant/issues)
- Discussions: [GitHub Discussions](https://github.com/your-org/consonant/discussions)
- Email: support@consonant.dev