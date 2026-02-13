# Changelog

All notable changes to Beam will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial open source release
- Core balance management system
- gRPC API with Protocol Buffers
- REST API for HTTP/JSON access
- CLI tool for administrative operations
- Docker and docker-compose support
- PostgreSQL with TimescaleDB for storage
- Redis for sub-millisecond balance checks
- Lua scripts for atomic operations
- Comprehensive test suite
- API documentation
- CI/CD with GitHub Actions
- Prometheus metrics integration
- Health check endpoints

### Security
- API key authentication with SHA-256 hashing
- TLS support for production
- Non-root Docker container

## [1.0.0] - 2025-01-XX

### Added
- Production-ready real-time AI cost enforcement
- CheckBalance operation (pre-flight validation)
- DeductTokens operation (real-time streaming deduction)
- FinalizeRequest operation (reconciliation)
- Automatic reservation system to prevent race conditions
- Multi-provider support (OpenAI, Anthropic, Google AI)
- Complete audit trail via transactions table
- Balance integrity verification
- Redis-PostgreSQL synchronization
- Load testing scripts
- Comprehensive documentation

### Changed
- Project renamed from Consonant to Beam
- Fully open source under MIT license
- Standalone operation without SDK requirement
- REST API added for easier integration

### Performance
- Sub-5ms balance checks
- Sub-3ms token deductions
- Support for 10,000+ concurrent requests
- 100,000+ operations/second with horizontal scaling

---

## Release Notes

### v1.0.0 - Initial Release

**Beam** is a production-grade system for enforcing AI spending limits in real-time. It sits between your application and AI providers to prevent customers from exceeding their allocated budgets.

**Key Features:**
- ⚡ Real-time balance checking and enforcement
- 🔒 Atomic operations prevent race conditions
- 💾 Dual storage (Redis + PostgreSQL) for speed and durability
- 🔌 Both gRPC and REST APIs
- 🛠️ CLI tool for operations
- 🐳 Docker ready
- 📊 Built-in metrics and monitoring

**Getting Started:**
```bash
git clone https://github.com/kelpejol/beam
cd beam
docker-compose up -d
make build
./backend/bin/beam-api
```

**Documentation:**
- [README.md](README.md) - Quick start and overview
- [CONTRIBUTING.md](CONTRIBUTING.md) - How to contribute
- [docs/API.md](docs/API.md) - Complete API reference
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) - System design

**Breaking Changes:**
- None (initial release)

**Known Issues:**
- None

**Contributors:**
Thank you to all contributors who made this release possible!

---

## Version History

- **v1.0.0** - Initial open source release

[Unreleased]: https://github.com/kelpejol/beam/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/kelpejol/beam/releases/tag/v1.0.0