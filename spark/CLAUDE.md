# Spark Backend (Go)

Go backend for Spark operators. See the parent [`CLAUDE.md`](../CLAUDE.md) for general code quality guidelines.

## Commands

### Building
```bash
make              # Generate proto files
mise gen-ent      # Generate Ent entities after schema changes
```

### Testing
```bash
mise test-unit                 # Fast unit tests (no postgres)
mise test-unit-with-postgres   # Unit tests requiring postgres features
mise test-grpc                 # Integration/gRPC tests (requires environment)

go test ./path/to/package -run TestName   # Single test
go test -v ./path/to/package              # Verbose output
```

### Linting
```bash
mise lint         # or: golangci-lint run
mise format       # or: golangci-lint fmt
```

### Database Migrations
```bash
./scripts/gen-migration.sh <migration_name>   # After schema changes
```

### Development Environment
```bash
./run-everything.sh                    # Local dev with tmux
./scripts/local-test.sh                # Hermetic testing with minikube
./scripts/build-to-minikube.sh         # Build image for minikube
```

## Architecture

- **`so/`** - Signing Operator implementation
  - **`handler/`** - gRPC request handlers
  - **`grpc/`** - gRPC server and interceptors
  - **`ent/`** - Database entities (generated, do not edit)
  - **`grpc_test/`** - Integration tests
- **`common/`** - Shared utilities
- **`proto/`** - Generated protobuf code (do not edit)

## Key Concepts

### FROST Threshold Signatures
Multiple operators collectively control Bitcoin UTXOs using threshold signatures.
- `common/secret_sharing/` - FROST implementation
- `so/handler/signing_handler/` - Signing coordination

### Transfers & Deposits
- `so/handler/deposit_handler.go` - Bitcoin deposits into Spark
- `so/handler/transfer_handler.go` - Off-chain transfers

### Token System (BTKN)
- `so/handler/tokens/` - Token transaction handlers
- `so/ent/schema/token_*.go` - Token entities

### Lightning Integration
- `so/handler/lightning_handler.go` - Lightning payments

## Database (Ent)

Key entities in `so/ent/schema/`:
- `transfer.go`, `tree.go`, `utxo.go` - Core entities
- `token_*.go` - Token entities
- `signing_*.go` - FROST signing state

### Schema Changes
1. Edit files in `so/ent/schema/`
2. Run `mise gen-ent`
3. Run `./scripts/gen-migration.sh <name>`

### Transaction Management
Transactions are automatically managed by the gRPC middleware (`so/grpc/database_middleware.go`):
- Commits on success, rolls back on error or panic
- Manual `tx.Commit()`/`tx.Rollback()` only needed in rare cases

## Testing

### Unit Tests (`*_test.go` alongside source)
- Fast, isolated tests
- Use `db.NewTestSQLiteContext()` for database tests
- Use `db.ConnectToTestPostgres()` for postgres-specific features

### Integration Tests (`so/grpc_test/`)
- End-to-end gRPC tests against running operators
- Use test wallet helpers from `testing/wallet/`
- Require environment setup (minikube or local)

### Patterns
- Table-driven tests for multiple scenarios
- Test fixtures with proper entity relationships
- Mock external dependencies (Bitcoin RPC, other operators)

## Common Patterns

### Error Handling
Use `so/errors` (as `sparkerrors`) to wrap errors with gRPC codes and reasons as close to the source as possible. Only wrap if the error doesn't already have a reason. If no appropriate reason exists in `error_types.go`, propose adding one.
```go
sparkerrors.InternalDatabaseReadError(fmt.Errorf("failed to query: %w", err))
sparkerrors.InvalidArgumentMalformedField(fmt.Errorf("invalid format: %s", val))
sparkerrors.FailedPreconditionInvalidState(fmt.Errorf("already completed"))
```

### Logging
```go
logger.Info("transfer completed", zap.String("transfer_id", id))
```

### Protocol Conversion
Convert between protobuf and internal types using `so/protoconverter/`

## Proto Changes
1. Edit `.proto` files in `protos/`
2. Run `make` (generates Go code)
3. Run `yarn generate:proto` in `sdks/js/packages/spark-sdk/` (generates TypeScript types)