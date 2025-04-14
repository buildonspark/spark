# Spark FROST Signing Service

This directory contains the implementation of the threshold signature scheme for Spark, using the Flexible Round-Optimized Schnorr Threshold (FROST) signature protocol with a tweak to enable a required participant.

## Project Structure

- **spark-frost/**: Core library implementing FROST primitives

  - **protos/**: Protocol buffer definitions for the signing service
  - **src/**: Core implementation
    - **signing.rs**: Implementation of FROST signing and aggregation functions
    - **lib.rs**: Core library exports
    - **proto.rs**: Protocol buffer handling
    - **frost_test.rs**: Unit tests

- **spark-frost-signer/**: gRPC service implementation
  - **src/**: Service implementation
    - **main.rs**: Entry point for the service
    - **server.rs**: gRPC service implementation
    - **dkg.rs**: Distributed Key Generation implementation

## Key Functionality

### Distributed Key Generation (DKG)

The DKG protocol is a three-round process that allows multiple parties to collectively generate key shares for a threshold signature without any single party knowing the complete private key:

1. **Round 1**: Each participant generates their initial key material and commitments
2. **Round 2**: Participants exchange commitments and generate secret shares
3. **Round 3**: Participants finalize the key generation, resulting in individual key packages

### FROST Signing

The signing process consists of two rounds:

1. **Nonce Generation**: Each signer generates a nonce and corresponding commitments
2. **Signature Share Creation**: Each signer creates their signature share using their private key share

### Signature Aggregation

The aggregation process combines signature shares from multiple signers to create a complete threshold signature.

## gRPC API

The service exposes the following gRPC endpoints:

- **echo**: Test endpoint for connectivity checks
- **dkg_round1**, **dkg_round2**, **dkg_round3**: Endpoints for the Distributed Key Generation protocol
- **frost_nonce**: Generates nonces needed for the signing process
- **sign_frost**: Creates signature shares for a given message
- **aggregate_frost**: Combines signature shares to create a complete signature
- **validate_signature_share**: Validates a signature share from a participant

## Usage

### Building the Service

```bash
cd signer
cargo build --release
```

### Running the Service

The service can listen on either a TCP port or a Unix domain socket:

```bash
# Listen on TCP port 8080
./target/release/spark-frost-signer --port 8080

# Listen on a Unix domain socket
./target/release/spark-frost-signer --unix /path/to/socket
```

### Integration with Spark

This signing service is designed to be used with the Spark protocol for secure distributed key management and transaction signing. It provides the cryptographic foundation for secure multi-party operations in the Spark protocol.

## Security Features

- **Threshold Security**: No single party has access to the complete private key
- **Distributed Key Generation**: Keys are generated collectively without a trusted dealer
- **Signature Verification**: Includes validation mechanisms for signature shares

## Implementation Details

The implementation uses:

- **frost-secp256k1-tr**: FROST implementation for secp256k1 with taproot support
- **tonic**: For gRPC service implementation
- **tokio**: For asynchronous runtime

## Additional Notes

- The service supports adaptive threshold signatures, where different subsets of participants can collaborate to create valid signatures.
- The implementation supports adaptor signatures, which are used for conditional transactions in the Spark protocol.
