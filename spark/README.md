# Spark Core Package

This is the core Go implementation of the Spark protocol, providing a modular architecture for Spark, a bitcoin layer 2 scaling solutions.

## Directory Structure

- **bin/**: Entry point binaries
  - **operator/**: The Spark operator service implementation
  - **user_wallet/**: User wallet client implementation
- **common/**: Shared utility functions and primitives
  - **secret_sharing/**: Secret sharing implementation for secure distributed systems
- **proto/**: Protocol buffer definitions and generated code

  - Auto-generated gRPC client/server code for all services

- **so/**: Server operator implementation
  - **authn/**: Authentication services
  - **authz/**: Authorization services
  - **dkg/**: Distributed key generation implementation
  - **ent/**: Entity framework for database models
  - **grpc/**: gRPC server implementations
  - **handler/**: Request handlers for different protocol operations
  - **tree/**: Tree data structure implementation for transaction management
- **test_util/**: Testing utilities and helpers

- **wallet/**: Wallet implementation for end users
  - **ssp_api/**: Service Provider API integration

## Getting Started

To build and run the operator service:

```bash
cd bin/operator
go build
./main
```

To build and run the user wallet:

```bash
cd bin/user_wallet
go build
./main
```

## Testing

Run tests with:

```bash
go test ./...
```

## License

See the LICENSE file in the root directory.
