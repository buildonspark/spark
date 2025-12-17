# Spark Protocol - Developer Context Guide

> **Code Locations**: See `spark_config.json` for all codebase paths

## 0. Configuration Setup

### spark_config.json Structure

The `spark_config.json` file contains codebase locations. Since this config is now inside the Spark repository, SO, SDK, and proto paths are automatically determined from the repository root. Only the SSP path needs to be configured.

**Path Resolution:**
- **SO, SDKs, and protos**: Paths are automatically determined relative to the Spark repository root (where `.claude/spark_config.json` is located)
- **SSP**: Has a `path` field (absolute path to the webdev repository)
- All `key_directories` are **relative to their component's base path**

**Example:**
```json
{
  "codebase_locations": {
    "so": {
      "key_directories": {
        "handlers": "so/handler",      // Resolved as: {REPO_ROOT}/spark/so/handler
        "signer": "../signer"          // Resolved as: {REPO_ROOT}/signer
      }
    },
    "ssp": {
      "path": "/Users/kph/code/webdev",
      "key_directories": {
        "handlers": "sparkcore/spark/handlers"  // Resolved as: /Users/kph/code/webdev/sparkcore/spark/handlers
      }
    }
  }
}
```

**When updating for your environment:**
1. Update only the `ssp.path` field to point to your webdev repository
2. Leave all `key_directories` as relative paths
3. SO, SDK, and proto paths are automatically determined - no configuration needed

**For AI assistants:**
- Repository root = directory containing `.claude/spark_config.json`
- SO path = `{REPO_ROOT}/spark/` (SO code is in spark/ subdirectory)
- SDKs path = `{REPO_ROOT}/sdks/`
- Protos path = `{REPO_ROOT}/protos/`
- Signer path = `{REPO_ROOT}/signer/`
- SSP path = read from `codebase_locations.ssp.path`
- Construct full paths by joining base path with relative `key_directories`
- When adding new key_directories, always use relative paths
- Never update key_directories to absolute paths

## 1. What is Spark?

Spark is a **Bitcoin Layer 2 scaling solution** based on **statechains** that enables off-chain transfer of UTXO ownership via cryptographic key updates rather than on-chain transactions.

### Core Value Proposition

Spark is a Bitcoin-native platform that enables developers to build financial applications and launch assets directly on Bitcoin without bridging or wrapping. It provides the fastest, lightest, and most developer-friendly way to:

- Build self-custody wallets with Bitcoin and stablecoin support
- Create and distribute Bitcoin-native assets (tokens)
- Enable instant, near-zero cost Bitcoin transfers
- Develop financial applications on Bitcoin infrastructure
- Integrate with Lightning Network for payment routing

### Core Primitive: Statechains

**How it works:**
- A UTXO is locked by a combined public key: `PubKey_Combined = PubKey_User + PubKey_SE`
- Ownership transfers off-chain by "tweaking" the Spark Entity's (SE) key share
- This invalidates the previous state without requiring an on-chain transaction

**Trust Model:**
- **1-of-N trust assumption**: If at least one Spark Operator (SO) deletes the old key share after a transfer, the previous owner cannot double-spend
- Users always retain **unilateral exit capability** through pre-signed timelock transactions

**Liveness Guarantee:**
- If the SE halts, users can recover funds via pre-signed **Unilateral Exit** transactions after a timelock

## 2. System Architecture

### Core Components

```
┌─────────────────────────────────────────────────────────────┐
│                     Bitcoin Layer                            │
│  - P2TR (Taproot) addresses                                 │
│  - Timelock-based exit transactions                         │
└─────────────────────────────────────────────────────────────┘
                            ▲
                            │
    ┌───────────────────────┼───────────────────────┐
    │                       │                       │
┌───▼──────┐        ┌──────▼──────┐        ┌──────▼──────┐
│  SO #1   │◄──────►│  SO #2      │◄──────►│  SO #N      │
│ (Go)     │        │ (Go)        │        │ (Go)        │
│          │        │ Coordinator │        │             │
│ Postgres │        │ Postgres    │        │ Postgres    │
└────┬─────┘        └──────┬──────┘        └──────┬──────┘
     │                     │                       │
     │         Gossip Protocol (Internal gRPC)     │
     └─────────────────────┼───────────────────────┘
                           │
                    ┌──────▼───────┐
                    │     SSP      │
                    │  (Python)    │
                    │  GraphQL API │
                    │  Postgres    │
                    └──────┬───────┘
                           │
                    ┌──────▼───────┐
                    │ Client SDKs  │
                    │ TS / Go / Rust│
                    └──────────────┘
```

### 1. Signing Operators (SOs)

**Location:** Spark repository root (automatically determined from config location)

**Purpose:** Distributed cluster of independent nodes that collectively manage Bitcoin operations through threshold cryptography (FROST signatures)

**Key Responsibilities:**
- **Threshold Signing**: Multi-party FROST signature generation for Bitcoin transactions
- **Key Management**: Distributed keyshare storage and management (no single SO can sign alone)
- **State Coordination**: Consensus on transaction states via gossip protocol
- **Chain Monitoring**: Independent Bitcoin blockchain monitoring via ZMQ subscriptions

**Architecture:**
- **Language**: Go
- **Database**: PostgreSQL (independent per SO for Byzantine fault tolerance)
- **API**: gRPC (both public user-facing and internal SO-to-SO)
- **Security**: Session-based authentication + IP whitelisting for internal APIs

**SO Coordinator:**
- One SO acts as the primary interface for user operations
- Orchestrates operations across all SOs using two-phase commit protocol
- Manages distributed transactions to ensure atomic operations

### 2. Spark Service Provider (SSP)

**Location:** See `spark_config.json` → `codebase_locations.ssp.path`

**Purpose:** Provides liquidity, acts as a gateway for users, and facilitates atomic swaps (often acts as a Lightning node)

**Key Responsibilities:**
- **GraphQL API Layer**: User-facing API for client interactions
- **Liquidity Provider**: Manages pools of leaves for swaps and exits
- **Lightning Gateway**: Routes Lightning payments to/from Spark network
- **Swap Coordination**: Facilitates atomic swaps between users and SSP leaves

**Architecture:**
- **Language**: Python
- **Database**: PostgreSQL
- **API**: GraphQL mutations for user operations, gRPC client for SO communication
- **State Machines**: Async processing via Celery for multi-step operations

### 3. Client SDKs

**Location:** `{SPARK_REPO}/sdks` (automatically determined from config location)

**Purpose:** User-facing libraries for building Spark applications

**Available SDKs:**
- **TypeScript/JavaScript**: Web and Node.js applications
- **Go**: Backend services
- **Rust**: Performance-critical applications

**Key Features:**
- Transaction preparation and signing
- Leaf selection and management
- Connection management with failover
- Key management and security

### 4. Key Terminology

| Term | Definition |
|------|------------|
| **Spark Entity (SE)** | Federation of operators acting as server-side signer (collective term for all SOs) |
| **Spark Operator (SO)** | Individual node within the SE using threshold signing (t-of-n) |
| **SO Coordinator** | The SO that handles external requests and orchestrates multi-SO operations |
| **Spark Service Provider (SSP)** | Provides liquidity and acts as gateway for Lightning integration |
| **Leaves** | Individual UTXO-like units in Spark trees (terminal transactions owned by users) |
| **Trees (Factories)** | Single L1 UTXO split into tree structure for scalability |
| **Branches** | Intermediate transactions without timelocks (spendable by aggregate keys of leaves below) |
| **Timelocks** | Decreasing relative timelocks ensure current owner can exit before previous owners |
| **FROST** | Flexible Round-Optimized Schnorr Threshold signatures for distributed signing |
| **Key Tweaking** | Modifying SE key by scalar value to transfer ownership off-chain |
| **Key Splitting** | Parent key split into child keys where ∑Key_children = Key_parent |

## 3. Cryptography (Spark FROST)

Spark uses a modified **FROST** (Flexible Round-Optimized Schnorr Threshold) scheme:

**Key Operations:**
- **Key Aggregation**: `PubKey_Combined = PubKey_User + PubKey_SE`
- **Signing**: Requires User + threshold of SOs (t of n) to produce valid Schnorr signature
- **Key Tweaking**: SE key modified by scalar to transfer ownership: `Key_new = Key_old + tweak`
- **Key Splitting**: Parent key splits into children such that `∑Key_children = Key_parent`

**FROST Implementation:**
- Core implementation in Rust (`signer/` directory)
- FFI bindings for Go integration
- Two-round signing protocol (more efficient than ECDSA threshold schemes)

## 4. Core Transaction Lifecycles

### Deposit (L1 → Spark)

1. **Key Generation**: User and SE generate combined Pay-to-Taproot address
2. **Pre-signing**: *Before funding*, sign Branch Tx (no timelock) + Exit Tx (timelocked)
3. **Funding**: User broadcasts L1 deposit transaction
4. **Tree Creation**: SO creates tree structure and publishes to L1
5. **Confirmation**: After sufficient confirmations, leaves become available

**Flow Documentation:** See `.claude_code/flows/static_deposits.md` and `.claude_code/flows/non_static_deposits.md`

### Transfer (Off-chain)

1. **Tweak Calculation**: SE tweaks its key share so new combined key grants control to receiver
2. **Two-Phase Commit**:
   - **Phase 1 (Prepare)**: All SOs lock leaves, validate ownership and refunds
   - **Phase 2 (Commit)**: Apply VSS key tweaks, transfer ownership atomically
3. **Validation**: Receiver verifies SE deleted old key shares (1-of-N trust model)
4. **New Exit Tx**: Receiver gets new pre-signed exit transaction with shorter timelock

**Flow Documentation:** See `.claude_code/flows/transfers.md`

### Lightning Integration

Spark supports Lightning via **Atomic Swaps** with SSP:

**Lightning Send:**
1. Client initiates preimage swap with SO, locking leaves for payment hash
2. SSP pays Lightning invoice, learns preimage
3. SSP provides preimage to SO, claims locked leaves
4. **Flow Documentation:** See `.claude_code/flows/lightning_send_flow.md`

**Lightning Receive:**
1. Client generates preimage, requests invoice from SSP
2. SSP creates Lightning invoice, waits for payment
3. When paid, SSP transfers leaves to user, user provides preimage
4. **Flow Documentation:** See `.claude_code/flows/lightning_receive_flow.md`

### Withdrawal (Spark → L1)

**Cooperative Exit:**
1. User requests withdrawal, SSP creates connector transaction
2. User signs refund transactions, SSP finalizes and broadcasts exit
3. **Flow Documentation:** See `.claude_code/flows/coop_exit_detailed_flow.md`

**Unilateral Exit:**
1. User broadcasts pre-signed Branch Tx
2. Wait for timelock expiry
3. Broadcast Exit Tx to recover funds

## 5. Data Structures

### Spark Trees (Factories)

To scale, a single L1 UTXO is split into a tree structure:

```
         Root UTXO (L1)
              │
         ┌────┴────┐
      Branch     Branch
         │          │
    ┌────┴──┐   ┌──┴────┐
  Leaf   Leaf  Leaf   Leaf
  (User) (User) (User) (User)
```

**Leaves:**
- Terminal transactions owned by users (spendable funds)
- Each has decreasing timelock for exit priority

**Branches:**
- Intermediate transactions without timelocks
- Spendable only by aggregate sum of keys of leaves below

**Timelocks:**
- **Decreasing relative timelocks** ensure security
- Each transfer reduces timelock: Alice (100 blocks) → Bob (90 blocks)
- Current owner can exit to L1 before previous owner can claim funds

## 6. Database Architecture

### Design Principle: Byzantine Fault Tolerance Through Isolation

**Critical:** Each SO maintains a **completely independent database instance**. This is not a performance optimization—it's a fundamental security property.

**Why Independent Databases:**
- **Byzantine Fault Tolerance**: Prevents single points of failure and Byzantine attacks
- **No Shared State**: Database isolation prevents trust dependencies
- **Local Consistency**: Each SO maintains ACID guarantees independently
- **Distributed Consensus**: Cross-SO consistency via gossip protocol and two-phase commit

**Key Database Entities:**
- `TreeNode`: Bitcoin UTXO states and ownership
- `Transfer`: Multi-SO transfer operations with status tracking
- `TokenTransaction`: Token operation states
- `PreimageRequest`: Lightning payment coordination
- `UtxoSwap`: Static deposit swap operations

**Locking Strategy:**
```go
// Prevents double-spending through database locks
lockedLeaves, err := db.TreeNode.Query().
    Where(treenode.IDIn(leafIDs...)).
    ForUpdate().  // Critical: locks leaves during operations
    All(ctx)
```

## 7. Authentication & Authorization

### Two-Tier Security Model

**Level 1: User Identity-Based Authorization**
- **Authentication**: Session-based with cryptographic identity verification
- **Authorization**: Users can only operate on resources they own
- **Enforcement**: `authz.EnforceSessionIdentityPublicKeyMatches(ctx, config, userPubKey)`

**Level 2: IP-Based Authorization (Internal APIs)**
- **Purpose**: SO-to-SO communication security
- **Implementation**: VPC restriction (10.x.x.x) + IP allowlisting
- **Rationale**: Network segmentation for internal operations

### gRPC Interface Design

**Public APIs** (`spark.proto`):
- User-facing operations (transfers, deposits, lightning)
- Authentication required for all operations
- Rate limiting and input validation

**Internal APIs** (`spark_internal.proto`):
- SO-to-SO coordination
- IP-restricted access
- High-performance operations

## 8. Coordination Protocols

### Two-Phase Commit Protocol

Used for atomic distributed operations:

1. **Phase 1 (Prepare)**:
   - Coordinator validates operation
   - All SOs validate inputs and lock resources
   - Collect Ready/Abort responses

2. **Phase 2 (Commit/Rollback)**:
   - If all Ready: Coordinator sends Commit to all SOs
   - If any Abort: Coordinator sends Rollback to all SOs
   - Ensures atomic completion across distributed system

### Gossip Protocol

**Purpose:** Ensures eventual consistency when SOs miss operations

**Message Types:**
- Transfer operations (settle, rollback, finalize)
- Tree operations (creation, exit)
- Utility operations (cleanup, preimage sharing)

**Benefits:**
- **Fault Tolerance**: Handles temporary SO failures
- **Network Partition Recovery**: Syncs state after connectivity issues
- **Self-Healing**: System recovers from inconsistent states automatically

## 9. Token System (BTKN)

Spark supports native tokens (BTKN) as metadata on leaves:

**Capabilities:**
- **Mint**: Issuer creates new token outputs on existing leaves
- **Transfer**: Atomic token transfers between users
- **Freeze**: Issuer can freeze token outputs
- **Burn**: Issuer signs burn transaction to remove tokens from circulation

**Implementation:**
- Tokens stored as database entities linked to tree nodes
- Token operations coordinated via two-phase commit across SOs
- Token metadata does not appear on Bitcoin blockchain

**Flow Documentation:** See `.claude_code/flows/token_creation_flow_CORRECTED.md` and related token flow docs

## 10. Code Organization

### SO Implementation (Go)

**Location:** Spark repository root

```
spark/so/
├── handler/              # gRPC handler implementations
│   ├── transfer_handler.go
│   ├── lightning_handler.go
│   ├── deposit_handler.go
│   └── coop_exit_handler.go
├── ent/                  # Database ORM (Ent framework)
│   ├── schema/          # Entity schemas and constraints
│   └── [generated]/     # Ent generated code
├── chain/               # Bitcoin chain monitoring
│   └── watch_chain.go   # Transaction confirmation tracking
└── authz/              # Authorization enforcement
```

### SSP Implementation (Python)

**Location:** See `spark_config.json` → `codebase_locations.ssp`

```
sparkcore/
├── spark/
│   ├── handlers/        # Business logic handlers
│   │   ├── swap_handler.py
│   │   ├── transfer_handler.py
│   │   └── lightning_handler.py
│   └── clients/         # SO client connections
│       └── spark_client.py
├── entities/            # Database models
└── graphql/
    └── mutations/       # GraphQL API mutations
```

### Client SDK (TypeScript)

**Location:** `{SPARK_REPO}/sdks/js/packages/spark-sdk`

```
spark-sdk/src/
├── services/            # Core service implementations
│   ├── transfer.ts
│   ├── deposit.ts
│   └── lightning.ts
├── graphql/            # GraphQL client code
└── wallet/             # Wallet management
```

## 11. Development & Testing

### Worktree Management

**CRITICAL**: When working with git worktrees in a session, ALWAYS work within the worktree directories, not the main repository.

**Session State Tracking:**
- After creating worktrees with `/setup_worktrees`, record the worktree paths
- All file operations, commands, and tool usage MUST occur within worktree directories
- When compacting or saving session state, preserve active worktree information
- At the start of each session interaction, verify you're using the correct worktree paths

**Active Worktree Paths** (when applicable):
- SO Worktree: Use the path from worktree creation (e.g., `/Users/kph/code/spark/spark2/spark-{branch}_so`)
- SSP Worktree: Use the path from worktree creation (e.g., `/Users/kph/code/lightsparkdev/webdev-{branch}_ssp`)

**Important Rules:**
1. Once a worktree is created in a session, NEVER work in the main repository
2. All `cd` commands must target worktree paths
3. All file reads/edits must use worktree file paths
4. Code quality commands must run from within worktree directories
5. Git operations (commit, status, etc.) must run from within worktrees

**Example - After `/setup_worktrees feature-x`:**
```bash
# CORRECT - Work in worktree
cd /Users/kph/code/spark/spark2/spark-feature-x_so
mise lint

# INCORRECT - Don't work in main repo
cd /Users/kph/code/spark/spark2/spark
mise lint
```

### Tooling

- **Manager**: `mise` for Go, Rust, Protobuf toolchain management
- **Hooks**: `lefthook` for pre-commit git hooks
- **Database**: PostgreSQL with `atlas` for schema migrations
- **Linting**: `golangci-lint` for Go code quality

### Testing Environment

- **Local Setup**: `./run-everything.sh` sets up:
  - Local Bitcoind (regtest)
  - PostgreSQL instances
  - 3 Signing Operators
  - SSP instance
- **Unit Tests**: `mise test-go` or `go test`
- **Integration Tests**: Located in `spark/so/grpc_test/`

### Debugging

- **Logs**: Uses Zap for structured logging
- **Tracing**: Look for `identity_public_key` in logs to trace user requests
- **Common Errors**: Signing errors often in aggregate signature calls - check sighash parity

## 12. Code Quality Commands

**CRITICAL**: After making ANY code changes, you MUST run the appropriate code quality commands for the repository you modified. These commands catch bugs, enforce consistency, and ensure code meets project standards.

### SO (Signing Operator) - Go

**Repository Path**: Spark repository root (automatically determined)

Run these commands from the Spark repository root:

**Linting** (Required):
```bash
mise lint
# OR
golangci-lint run
```

**Unit Tests** (Required):
```bash
mise test-go           # Quick tests (recommended)
# OR
mise test-unit         # Without postgres-dependent tests (~20-30s faster)
# OR
mise test-unit-with-postgres  # Full unit tests with postgres
```

**Proto Generation** (When proto files change):
```bash
make
```

**CRITICAL**: After running `make` to regenerate SO proto bindings, you MUST also regenerate JS SDK proto bindings:
```bash
cd sdks/js/packages/spark-sdk/
mise exec -- yarn generate:proto
```
**NOTE**: Must use `mise exec --` to ensure correct protoc version (29.3) is used. See section 13.4 for tool version requirements.

**Ent Generation** (When schema changes):
```bash
mise gen-ent
```

**Integration Tests** (Before PR):
```bash
mise test-grpc-minikube
# OR (from spark/ directory)
MINIKUBE_IP=$(minikube ip) go test -failfast=false -p=2 ./so/grpc_test/...
```

### SSP (Spark Service Provider) - Python

**Repository Path**: `codebase_locations.ssp.path` from `spark_config.json`

Run these commands from the `sparkcore/` directory:

**Formatting** (Required):
```bash
uv run ruff format .
# OR via Makefile
make run_black
```

**Linting** (Required):
```bash
uv run ruff check . --fix
# OR via Makefile
make run_ruff
```

**Type Checking** (Required):
```bash
uv run pyre
```

**Unit Tests** (Required):
```bash
env -u QUART_CONFIG pytest -m "not minikube"
```

**GraphQL Schema Export** (When schema changes):
```bash
uv run scripts/export-graphql.py
# OR via Makefile
make graphql
```

### Client SDK (TypeScript)

**Repository Path**: `codebase_locations.sdks.typescript` from `spark_config.json`

**CRITICAL**: The JS workspace is a monorepo with multiple packages. Always run workspace-level checks, not just package-level checks.

**Workspace-Level Build** (Required - Catches All Packages):
```bash
# From sdks/js/ directory (workspace root)
yarn build  # Builds SDK + examples + all packages
```

**Package-Level Checks** (Run from `sdks/js/packages/spark-sdk/` directory):

**Formatting** (Required):
```bash
yarn format:fix
# OR
prettier . --write
```

**Linting** (Required):
```bash
yarn lint:fix
# OR
eslint --fix .
```

**Type Checking** (Required):
```bash
yarn types
# OR
tsc
```

**Build** (Required):
```bash
yarn build
```

**Unit Tests** (Required):
```bash
yarn test
```

**Integration Tests** (Before PR):
```bash
yarn test:integration
```

**Proto Generation** (When proto files change):
```bash
mise exec -- yarn generate:proto
```
**CRITICAL**: When proto files (`.proto`) are modified in the SO codebase, you MUST regenerate the JS SDK proto bindings. Run this from `sdks/js/packages/spark-sdk/` directory after running `make` in the SO repository. Must use `mise exec --` to ensure correct protoc version (29.3) is used.

**Example Scripts Verification** (Required if examples modified):
```bash
# From sdks/js/examples/nodejs-scripts/ directory
yarn prettier --check .  # Check formatting (or --write to fix)
yarn types              # Type-check all example scripts
yarn build              # Verify examples compile
```

### Quick Reference: Minimum Required Checks

**After ANY code change, run AT MINIMUM:**

| Repository | Minimum Commands |
|------------|-----------------|
| **SO (Go)** | `mise lint && mise test-go` |
| **SSP (Python)** | `uv run ruff format . && uv run ruff check . --fix && uv run pyre && env -u QUART_CONFIG pytest -m "not minikube"` |
| **SDK (TypeScript)** | `cd sdks/js && yarn build` (workspace-level - catches all packages) |

**Detailed SDK Checks** (if modifying SDK code directly):
| Package | Commands |
|---------|----------|
| **spark-sdk** | `cd packages/spark-sdk && yarn format:fix && yarn lint:fix && yarn types && yarn build && yarn test` |
| **Examples** | `cd examples/nodejs-scripts && yarn prettier --write . && yarn types && yarn build` |

**Use `/clean_code` command**: Run `/clean_code so`, `/clean_code ssp`, or `/clean_code both` to automatically run all required checks.

## 13. Important Architectural Principles

### 1. Unilateral Exit Capability

**Critical:** Users possess **pre-signed exit transactions** that can be published to Bitcoin L1 **at any time** without SO or SSP interaction.

**For every security analysis, consider:**
- What happens if user publishes exit transactions during this flow?
- Are there timing windows where user could double-spend?
- Do timelocks provide adequate protection for all parties?

### 2. Never Trust Client Data

**Fundamental rule:** The SDK is a reference implementation. Assume malicious clients will:
- Write custom SDKs that skip validation
- Call SSP/SO APIs directly with crafted payloads
- Reverse-engineer protocols to bypass intended flows

**Server must always:**
- Validate all inputs server-side
- Reconstruct transactions from minimal validated parameters
- Never accept raw transaction bytes from clients
- Verify all amounts, addresses, and cryptographic commitments

### 3. Database Consistency in Distributed Systems

**Challenges:**
- Each SO has independent database
- Network partitions can cause temporary inconsistencies
- Gossip protocol provides eventual consistency

**Best practices:**
- Use `ForUpdate()` locks for critical operations
- Wrap multi-step operations in database transactions
- Design for idempotency and retry safety
- Consider crash recovery at every step

### 4. Respect Tool Versions and Configuration

**Critical:** Never update tool versions or configuration values without explicit user consent.

**Rules:**
- Always respect version numbers specified in configuration files (e.g., `.tool-versions`, `mise.toml`, proto files)
- Never modify tool versions (e.g., protoc, Go, Node, Python versions) during implementation
- If a version upgrade is needed for a feature, explicitly ask the user first
- When generating code, preserve the existing version numbers in generated comments/headers
- Configuration files are source of truth - do not second-guess or "improve" them

**Examples:**
- ✅ Use protoc version specified in config: `protoc v5.29.3`
- ❌ Update to latest protoc version without asking: `protoc v5.30.0`
- ✅ Ask: "This feature requires Go 1.22+, but your config specifies 1.21. Should I update?"
- ❌ Silently update Go version in go.mod

### 5. No Mock Implementations in Production Code

**Critical:** Never create mock or stub implementations in production code. Only use mocks in test cases.

**Rules:**
- Production code must always contain complete, functional implementations
- Empty functions, stub returns, or placeholder implementations are prohibited
- Only test files (e.g., `*_test.go`, `*.test.ts`) may contain mocks
- If a function cannot be fully implemented, discuss with the user rather than stubbing it out

**Examples:**
- ✅ In `foo_test.go`: `func mockTransfer() *Transfer { return &Transfer{...} }`
- ❌ In `foo.go`: `func processTransfer() { /* TODO: implement */ }`
- ✅ In test: `client := &MockSparkClient{...}`
- ❌ In production: `func validateSignature() bool { return true } // TODO: add validation`

**Why this matters:**
- Mock implementations can accidentally ship to production
- Incomplete code creates security vulnerabilities
- Tests may pass against mocks but fail in production
- Creates false confidence in code completeness

## 14. Known Limitations & Beta Status

- **Beta Network**: Expect breaking changes as protocol evolves
- **Leaf Expiry**: Leaves have finite lifespans due to decrementing timelocks
- **Refresh Required**: Leaves must be refreshed or extended periodically (requires re-anchoring)
- **Watchtowers**: Required to prevent previous owners from broadcasting old states
- **Timelock Windows**: If old state broadcast, current owner must respond within timelock window

## 15. Flow Documentation Structure

All flow analyses are documented in `.claude_code/flows/`:

**Core Flows:**
- `transfers.md` - Standard Spark-to-Spark transfers
- `lightning_send_flow.md` - Pay Lightning invoices with Spark leaves
- `lightning_receive_flow.md` - Receive Lightning payments to Spark leaves
- `static_deposits.md` - Deposit to pre-generated addresses
- `non_static_deposits.md` - Deposit to one-time addresses
- `coop_exit_detailed_flow.md` - Cooperative withdrawals to L1
- `swap_counterswap_flow.md` - Atomic leaf swaps

**Token Flows:**
- `token_creation_flow_CORRECTED.md` - Token issuance
- `token_minting_flow_CORRECTED.md` - Minting new token outputs
- `token_transfer_flow_CORRECTED.md` - Token transfers between users

**System Architecture:**
- `spark_system_architecture.md` - Comprehensive system overview
- `ssp_system_architecture.md` - SSP-specific architecture
- `so_architecture_diagram.md` - SO cluster architecture

## 16. Security Review Resources

**Primary Security Documents:**
- `.claude_code/spark_security_review_guide.md` - Methodology and checklist
- `.claude_code/security_framework.md` - Vulnerability categories and patterns

**Key Security Principles:**
1. Always start client-side and trace through entire system
2. Check authentication/authorization for every endpoint first
3. Never trust raw transaction bytes from clients
4. Consider unilateral exit scenarios for every flow
5. Analyze race conditions in multi-step database operations
6. Verify timelock ordering and expiry handling
7. Check for TOCTOU (Time-of-Check-to-Time-of-Use) vulnerabilities

## 17. Getting Started Checklist

When analyzing Spark code or flows:

1. **Load Configuration**: Reference `spark_config.json` for all code paths
2. **Identify Component**: Determine if analyzing SO, SSP, or SDK code
3. **Find Flow Documentation**: Check `.claude_code/flows/` for relevant flow
4. **Understand Authentication**: Know which auth model applies (identity vs IP)
5. **Consider Unilateral Exits**: Always think about user exit scenarios
6. **Check Database Locking**: Look for `ForUpdate()` and transaction boundaries
7. **Trace Complete Path**: Follow from SDK → SSP → SO → Bitcoin

## 18. Key Contact Points in Codebase

**Starting a transfer:**
- Client: `transfer.ts:243` - `sendTransferWithKeyTweaks()`
- SO: `transfer_handler.go:468` - `startTransferInternal()`

**Lightning send:**
- Client: `spark-wallet.ts:3338` - `payLightningInvoice()`
- SO: `lightning_handler.go` - `initiate_preimage_swap_v2()`
- SSP: State machine in `SparkLightningSendStateMachine`

**Deposits:**
- Client: `deposit.ts:145` - `generateStaticDepositAddress()`
- SO: `deposit_handler.go` - `generate_static_deposit_address()`
- SSP: `claim_static_deposit_fixed_amount` mutation

**Cooperative exit:**
- Client: `spark-wallet.ts:3912` - `withdraw()`
- SSP: `request_coop_exit` mutation
- SO: `coop_exit_handler.go` - `CooperativeExit()`

---

**Philosophy:** "Bitcoin is the only network that will likely be around on every timeline; it's the natural bedrock for a radically new global payment network."

This guide provides the foundational context for understanding Spark's architecture, codebase structure, and development practices. For detailed security analysis, flow-specific documentation, and implementation details, reference the specialized documents in `.claude_code/`.
