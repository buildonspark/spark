# Spark Operator Service (SO)

The Server Operator component of the Spark protocol, responsible for managing state, DKG, and transaction processing as well as signing.

## Architecture

The SO module is structured around multiple components:

- **authn/**: Authentication service and middleware
  - Handles user authentication and session management
- **authz/**: Authorization service

  - Controls access permissions for different operations

- **dkg/**: Distributed Key Generation

  - Implements threshold cryptography for distributed key management
  - Coordinator for multi-party computation protocols

- **ent/**: Database Entity Framework

  - Object-relational mapping for the underlying database
  - Schema definitions and migrations
  - Query builders and transaction management

- **grpc/**: gRPC Server Implementations

  - Server implementations for all protocol APIs
  - Error handling and validation interceptors

- **handler/**: Request Handlers

  - Deposit handling
  - Transfer processing
  - Transaction lifecycle management
  - Tree operations
  - Lightning network integration

- **tree/**: Tree Data Structure

  - Tree-based transaction management
  - Solver for finding optimal transaction paths
  - Helper functions for tree traversal and manipulation

- **utils/**: Utility Functions
  - Token transaction utilities
  - Identifier management

## Configuration

The service is configured via the `config.go` file, which defines the configuration structure for the SO service.

## Integration Testing

The `grpc_test/` directory contains integration tests for the SO service, covering:

- Cooperative exits
- Deposits
- Distributed key generation
- Lightning network integration
- Timelock refreshing
- Signing operations
- Token transactions
- Transfers
- Tree operations

## Getting Started

To run the SO service, you need to:

1. Configure the database connection
2. Set up the gRPC server credentials
3. Initialize the entity framework
4. Start the service

See the main application entry point in `bin/operator/main.go` for implementation details.

## Development

When developing new features, follow these guidelines:

1. Add appropriate test cases in the `grpc_test/` directory
2. Update schema definitions in `ent/schema/` when modifying database models
3. Implement handlers for new operations in the `handler/` directory
4. Update gRPC server implementations in the `grpc/` directory
