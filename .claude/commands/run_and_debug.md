---
description: Start local dev environment, run integration tests, and debug SO handler code
---

# Run and Debug

Manages the local development environment for running and debugging Signing Operator (SO) code and integration tests.

## Usage

```bash
/run_and_debug start       # Start the local environment (run-everything.sh)
/run_and_debug test <name> # Run a specific integration test
/run_and_debug test        # Run all integration tests
/run_and_debug logs        # Show recent operator logs
/run_and_debug cleanup     # Nuke everything and start fresh
```

## How the Local Environment Works

### Starting (`./run-everything.sh`)

`run-everything.sh` builds and starts all components locally in tmux:

- **Builds the operator binary** from Go source to `_data/run_X/bin/operator`
- **Starts 5 Signing Operators** (SO 0-4) on ports 9001-9005
- **Starts 5 FROST signers** (Rust) on ports 50051-50055
- **Starts bitcoind** in regtest mode
- **Starts electrs** (Electrum server)
- **Creates PostgreSQL databases** for each operator

Each run creates an incrementing `_data/run_X/` directory containing:
```
_data/run_X/
├── bin/operator              # Built operator binary
├── config.json               # Operator config
├── db/                       # PostgreSQL data
├── logs/
│   ├── sparkoperator_0.log   # SO 0 logs
│   ├── sparkoperator_1.log   # SO 1 logs
│   ├── ...
│   ├── signer_0.log          # FROST signer logs
│   ├── ...
│   ├── bitcoind.log
│   └── electrs.log
├── operator_*.key            # Operator keys
└── server_*.crt/key          # TLS certs
```

### Running Integration Tests

With the environment running, integration tests in `spark/so/grpc_test/` can be executed:

```bash
# Run a specific test
cd spark && go test -v -run TestName ./so/grpc_test/...

# Run all integration tests
cd spark && go test -v ./so/grpc_test/...

# Run with timeout for long tests
cd spark && go test -v -timeout 120s -run TestName ./so/grpc_test/...
```

No `MINIKUBE_IP` is needed for the local environment.

### Cleanup (`./cleanup.sh`)

When things go wrong (stuck processes, corrupt DB, etc.), `cleanup.sh` kills all tmux sessions:
- frost-signers, operators, lrcd, electrs, bitcoind

After cleanup, run `./run-everything.sh` again to start fresh.

### Key Gotcha: Server Binary Rebuild

**Handler code changes require re-running `run-everything.sh`.** The running operators use a pre-built binary at `_data/run_X/bin/operator`. When you modify SO handler code (e.g., `spark/so/handler/*.go`), those changes won't take effect until `run-everything.sh` rebuilds the binary and restarts the operators.

Test client code in `spark/so/grpc_test/` is recompiled by `go test` on every run, so test changes take effect immediately.

## Implementation Instructions

When this command is invoked:

1. **Parse the parameter** to determine the action: `start`, `test`, `logs`, or `cleanup`.

2. **If no parameter provided:**
   ```
   Please specify an action.

   Usage: /run_and_debug <start|test|logs|cleanup>

   Examples:
   - /run_and_debug start           # Start local environment
   - /run_and_debug test TestTransfer  # Run a specific test
   - /run_and_debug test             # Run all integration tests
   - /run_and_debug logs             # Show recent operator logs
   - /run_and_debug cleanup          # Kill everything and start fresh
   ```
   Stop and wait for user input.

3. **Action: `start`**
   - Run `./run-everything.sh` from the repository root
   - This takes a minute or so to build and start everything
   - Confirm the environment is running by checking tmux sessions

4. **Action: `test [TestName]`**
   - If a test name is provided, run that specific test:
     ```bash
     cd spark && go test -v -timeout 120s -run TestName ./so/grpc_test/...
     ```
   - If no test name, run all integration tests:
     ```bash
     cd spark && go test -v -timeout 300s ./so/grpc_test/...
     ```
   - If a test fails:
     - Read the test output for the error message
     - Find the latest run directory: `ls -td _data/run_* | head -1`
     - Check relevant operator logs for server-side errors:
       ```bash
       # Check coordinator (SO 0) logs for errors around test time
       grep -i "error\|panic\|fatal" _data/run_X/logs/sparkoperator_0.log | tail -20
       ```
     - If the error is in handler code that was recently modified, remind the user:
       "Handler code changes require re-running `./run-everything.sh` to rebuild the operator binary."

5. **Action: `logs`**
   - Find the latest run directory: `ls -td _data/run_* | head -1`
   - Show the last 50 lines of SO 0 (coordinator) logs:
     ```bash
     tail -50 _data/run_X/logs/sparkoperator_0.log
     ```
   - If the user asks about a specific operator, show that operator's logs instead

6. **Action: `cleanup`**
   - Run `./cleanup.sh` from the repository root
   - Confirm processes are killed
   - Inform the user they can run `/run_and_debug start` to start fresh

## Debugging Tips

### Finding the right logs
- **SO 0** is the coordinator and handles most external requests
- Check `sparkoperator_0.log` first for transfer/deposit/lightning errors
- Check signer logs (`signer_X.log`) for FROST signing failures
- Use `grep` with transfer IDs or identity public keys to trace requests

### Common issues
- **"connection refused"**: Environment isn't running. Run `/run_and_debug start`
- **"transaction already committed"**: Ent entity used after tx commit. Reload from fresh DB client
- **Test passes locally but handler code change not reflected**: Need to re-run `run-everything.sh`
- **Stuck state / corrupt DB**: Run `/run_and_debug cleanup` then `/run_and_debug start`
