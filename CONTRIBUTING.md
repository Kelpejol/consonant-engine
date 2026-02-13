# Contributing to Beam

First off, thank you for considering contributing to Beam! It's people like you that make Beam such a great tool.

## Code of Conduct

This project and everyone participating in it is governed by our Code of Conduct. By participating, you are expected to uphold this code. Please report unacceptable behavior to the project maintainers.

## How Can I Contribute?

### Reporting Bugs

Before creating bug reports, please check the existing issues as you might find that you don't need to create one. When you are creating a bug report, please include as many details as possible:

- **Use a clear and descriptive title**
- **Describe the exact steps to reproduce the problem**
- **Provide specific examples** to demonstrate the steps
- **Describe the behavior you observed** and what you expected
- **Include logs and error messages**
- **Specify your environment** (OS, Go version, Docker version, etc.)

### Suggesting Enhancements

Enhancement suggestions are tracked as GitHub issues. When creating an enhancement suggestion, please include:

- **Use a clear and descriptive title**
- **Provide a detailed description** of the proposed functionality
- **Explain why this enhancement would be useful**
- **List any alternative solutions** you've considered

### Pull Requests

1. **Fork the repository** and create your branch from `main`
2. **Follow the coding style** of the project (see below)
3. **Write tests** for new features
4. **Update documentation** as needed
5. **Ensure tests pass** (`make test`)
6. **Write meaningful commit messages**
7. **Open a Pull Request** with a clear description

## Development Setup

### Prerequisites

- Go 1.25 or higher
- Docker and Docker Compose
- Make
- protoc (Protocol Buffer compiler)

### Getting Started

```bash
# 1. Fork and clone the repository
git clone https://github.com/YOUR_USERNAME/beam.git
cd beam

# 2. Start infrastructure
docker-compose up -d postgres redis

# 3. Install dependencies
go mod download

# 4. Generate protobuf code
make proto

# 5. Build the project
make build

# 6. Run tests
make test
```

## Coding Standards

### Go Code Style

- Follow [Effective Go](https://golang.org/doc/effective_go) guidelines
- Use `gofmt` for formatting (run `make format`)
- Use `golangci-lint` for linting (run `make lint`)
- Write meaningful variable and function names
- Add comments for exported functions and types
- Keep functions small and focused (< 50 lines)

### Testing

- Write unit tests for new features
- Maintain test coverage above 80%
- Use table-driven tests where appropriate
- Mock external dependencies
- Name tests clearly: `TestFunctionName_Scenario_ExpectedBehavior`

Example:
```go
func TestLedger_CheckBalance_InsufficientFunds_ReturnsRejected(t *testing.T) {
    // Test implementation
}
```

### Commit Messages

Use conventional commits format:

```
<type>(<scope>): <subject>

<body>

<footer>
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Adding or updating tests
- `chore`: Maintenance tasks

Examples:
```
feat(api): add REST endpoint for balance queries

Implements GET /v1/balance/:customer_id endpoint to allow
easy balance checking without gRPC client.

Closes #123
```

```
fix(ledger): prevent race condition in reservation

Uses Redis Lua script to atomically check and reserve balance,
preventing double-spending when concurrent requests arrive.

Fixes #456
```

## Project Structure

```
beam/
├── backend/
│   ├── cmd/
│   │   ├── api/          # Server entrypoint
│   │   └── cli/          # CLI tool
│   ├── internal/         # Private application code
│   │   ├── api/          # gRPC handlers
│   │   ├── rest/         # REST handlers
│   │   ├── auth/         # Authentication
│   │   ├── ledger/       # Core business logic
│   │   └── sync/         # Data synchronization
│   ├── pkg/              # Public libraries
│   │   └── proto/        # Generated protobuf code
│   └── migrations/       # Database migrations
├── scripts/              # Helper scripts
├── docs/                 # Documentation
└── tests/                # Integration tests
```

## Adding New Features

### 1. API Changes

If adding a new gRPC endpoint:

1. Update `proto/balance/v1/balance.proto`
2. Run `make proto` to regenerate code
3. Implement the handler in `internal/api/`
4. Add REST endpoint in `internal/rest/`
5. Update CLI in `cmd/cli/` if needed
6. Write tests
7. Update documentation

### 2. Database Changes

If modifying the database schema:

1. Create a new migration file in `migrations/`
2. Name it sequentially: `002_description.up.sql` and `002_description.down.sql`
3. Test both up and down migrations
4. Update schema documentation

### 3. Configuration Changes

If adding new configuration options:

1. Add to config struct in `cmd/api/main.go`
2. Add environment variable
3. Add to `.env.example`
4. Update documentation
5. Set reasonable defaults

## Testing Guidelines

### Unit Tests

```go
func TestNewLedger(t *testing.T) {
    tests := []struct {
        name    string
        setup   func()
        want    *Ledger
        wantErr bool
    }{
        {
            name: "successful initialization",
            setup: func() {
                // Setup test environment
            },
            want: &Ledger{},
            wantErr: false,
        },
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if tt.setup != nil {
                tt.setup()
            }
            
            got, err := NewLedger(...)
            if (err != nil) != tt.wantErr {
                t.Errorf("NewLedger() error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("NewLedger() = %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Integration Tests

Place in `tests/integration/` and require Docker:

```go
// +build integration

func TestBalanceFlow(t *testing.T) {
    // Start test containers
    // Test full flow
    // Cleanup
}
```

Run with: `make test-integration`

## Documentation

### Code Comments

- Document all exported types, functions, and constants
- Use complete sentences
- Explain the "why", not just the "what"

```go
// CheckBalance validates that a customer has sufficient balance for an AI request
// and atomically reserves the required grains to prevent race conditions.
// 
// The reservation prevents multiple concurrent requests from all checking the
// balance, seeing sufficient funds, and proceeding even though collectively
// they exceed available balance.
//
// Returns a secure token that must be included in subsequent DeductTokens calls.
func (l *Ledger) CheckBalance(ctx context.Context, req ReservationRequest) (*ReservationResult, error) {
    // Implementation
}
```

### Documentation Files

Update relevant docs in `docs/` when changing:
- API behavior
- Configuration options
- Architecture
- Integration steps

## Review Process

1. **Automated Checks**: CI runs tests, linting, and builds
2. **Code Review**: Maintainers review for quality and fit
3. **Discussion**: Address feedback and questions
4. **Approval**: Two approvals required for merge
5. **Merge**: Squash and merge to main branch

## Release Process

Releases are handled by maintainers:

1. Update `CHANGELOG.md`
2. Tag version: `git tag v1.2.3`
3. Push tag: `git push origin v1.2.3`
4. CI creates GitHub release automatically
5. Docker images published to registry

## Getting Help

- **Documentation**: Check the [docs/](docs/) directory
- **GitHub Issues**: Search existing issues
- **GitHub Discussions**: Ask questions
- **Discord**: Join our community (link in README)

## Recognition

Contributors are recognized in:
- `CONTRIBUTORS.md` file
- GitHub contributors page
- Release notes

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

---

Thank you for contributing to Beam! 🚀