# GEMINI.md

This file provides guidance to Gemini CLI when working with code in this repository.

## Essential Commands

### Setup
```bash
# Install toolchain and dependencies
mise trust
mise install

# Install git hooks (optional but recommended)
lefthook install
```

### Building
```bash
# Generate proto files (required after proto changes)
make

# Generate Ent entities (required after schema changes)
make ent
```

### Testing
```bash
# Unit tests (fast, no postgres dependencies)
mise test-unit

# Unit tests with postgres dependencies
mise test-unit-with-postgres

# Integration/gRPC tests (requires environment setup)
mise test-grpc

# Run a single test
go test ./path/to/package -run TestName

# Run tests with verbose output
go test -v ./path/to/package
```

### Linting
```bash
# Run Go linter
mise lint
# OR
golangci-lint run

# To fix linter issues
mise format
# OR
golangci-lint fmt
```

### Database Migrations
```bash
# After modifying Ent schemas, generate migration
./scripts/gen-migration.sh <migration_name>

# Apply migration manually (if needed)
atlas migrate apply --dir "file://so/ent/migrate/migrations" --url "postgresql://127.0.0.1:5432/sparkoperator_0?sslmode=disable"
```

### Development Environment
```bash
# Local development with tmux (requires setup)
./run-everything.sh

# Hermetic testing with minikube (recommended)
./scripts/local-test.sh

# Build image for minikube development
./scripts/build-to-minikube.sh
./scripts/local-test.sh --dev-spark
```

## Architecture Overview

### High-Level Structure
This repository implements **Spark**, a Bitcoin-based system with the following main components:

- **Signing Operators (SO)**: Distributed nodes that collectively manage Bitcoin transactions and cryptographic operations
- **Wallet**: Client-side interface for users to interact with the Spark network
- **Signer**: Rust-based FROST (Flexible Round-Optimized Schnorr Threshold) signing implementation
- **LRC20**: Layer for token functionality on top of Bitcoin
- **SDKs**: JavaScript and Rust SDKs for external integration

### Key Components

#### Spark Directory (`spark/`)
**Main Go module** containing the core Spark implementation:

- **`so/`** - Signing Operator implementation
  - **`handler/`** - gRPC request handlers for different operations (deposits, transfers, tree creation, etc.)
  - **`grpc/`** - gRPC server implementations for various services
  - **`ent/`** - Database entities and ORM (generated, do not edit directly)
  - **`chain/`** - Bitcoin blockchain interaction and ZMQ monitoring
  - **`dkg/`** - Distributed Key Generation coordination
  - **`helper/`** - Utility functions for operators and signing coordination
  - **`grpc_test/`** - Integration tests requiring full environment setup

- **`wallet/`** - Client-side wallet implementation
- **`common/`** - Shared utilities across the codebase
- **`proto/`** - Generated protobuf code (do not edit directly)

#### Signer Directory (`signer/`)
**Rust implementation** of FROST threshold signatures:
- **`spark-frost/`** - Core FROST signing logic
- **`spark-frost-uniffi/`** - UniFFI bindings for cross-language integration
- **`spark-frost-signer/`** - Standalone signer service

#### SDKs Directory (`sdks/`)
- **`js/`** - JavaScript/TypeScript SDK with multiple example applications
- **`rs/`** - Rust SDK for Spark integration

#### LRC20 Directory (`lrc20.dev/`)
**Rust-based token layer** with indexing and node functionality.

### Database Architecture
- Uses **Ent ORM** for database operations
- Supports PostgreSQL (production) and SQLite (testing)
- **Atlas** for schema migrations
- Database schemas defined in `spark/so/ent/schema/`

### Protocol Communication
- **gRPC** for inter-service communication
- **Protobuf** definitions in `protos/` directory
- Multiple service interfaces:
  - `SparkService` - Main user-facing API
  - `SparkInternalService` - Inter-operator communication
  - `GossipService` - Distributed state synchronization

### Testing Strategy
- **Unit tests** - Fast, isolated tests (suffix `_test.go`)
- **Integration tests** - Full system tests in `spark/so/grpc_test/`
- **Hermetic testing** - Complete environment in minikube
- Test database context helpers in `spark/so/db/testing.go`

## Development Guidelines

### Code Organization
- **Handlers** implement business logic for gRPC endpoints
- **Ent entities** represent database models (auto-generated)
- **Proto converters** transform between internal types and protobuf messages
- **Middleware** handles cross-cutting concerns (auth, logging, rate limiting)

### Common Patterns
- Database operations use Ent's fluent API
- Context-based request handling with proper cancellation
- Error wrapping with structured logging
- Transaction management through database sessions

### Testing Patterns
- Use `db.NewTestSQLiteContext()` for database-dependent tests
- Create test fixtures with proper entity relationships
- Table-driven tests for multiple similar test cases
- Separate unit tests from integration tests

### When Working with Database Code
- Modify schemas in `spark/so/ent/schema/`, then run `make ent`
- Generate migrations with `./scripts/gen-migration.sh` after schema changes
- Use proper foreign key relationships in Ent schemas
- Test database code with both SQLite (unit) and PostgreSQL (integration)

### Proto File Changes
- Edit `.proto` files in `protos/` directory
- Run `make` to generate Go code
- Update related Rust bindings if needed
- Regenerated files go into `spark/proto/` (do not edit directly)
