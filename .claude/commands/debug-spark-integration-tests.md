---
description: Rebuild minikube environment and run targeted SO integration tests with debugging support
---

# Debug Spark Integration Tests

Rebuilds the SO in minikube (when needed), starts the test environment, and runs targeted integration tests. Includes guidance on adding debug logging and collecting SO logs.

## Usage

```bash
/debug-spark-integration-tests <test-pattern>
/debug-spark-integration-tests              # Pull failing tests from PR CI
```

**Examples:**
```bash
/debug-spark-integration-tests TestMintTokens
/debug-spark-integration-tests all                   # Run all integration tests (slow)
/debug-spark-integration-tests                       # Auto-detect failures from PR
```

## Implementation Instructions

When this command is invoked:

### 1. Check minikube is running

Verify minikube is up:
```bash
minikube status
```

If minikube is not running, tell the user to start it:
```
minikube is not running. Start it with:

minikube delete && USE_LIGHTSPARK_HELM_REPO=true minikube/setup.sh
```

**Do not proceed** until minikube is confirmed running.

### 2. Pull failing tests from PR (when no test pattern provided)

If the user did not specify a test pattern, attempt to extract failing test names from CI:

```bash
# Get the latest failed run for the current branch
RUN_ID=$(gh run list --branch $(git branch --show-current) --limit 1 --json databaseId,conclusion --jq '.[0] | select(.conclusion == "failure") | .databaseId')

# Extract failing Go test names from CI logs
gh run view $RUN_ID --log-failed 2>/dev/null | grep -E -- '--- FAIL:' | sed 's/.*--- FAIL: //' | sed 's/ (.*//' | sort -u
```

- If failing test names are found, use them as the `-run` pattern
- If multiple tests failed, combine with regex: `-run "TestFoo|TestBar|TestBaz"`
- If no PR or no failed runs exist, ask the user which tests to run

### 3. Determine what changed

Check if SO code was modified by running:
```bash
git diff --name-only HEAD | grep -E '^spark/so/|^spark/common/|^signer/'
```

- If SO/signer files changed: rebuild is required
- If only test files changed: skip rebuild, go straight to running tests

### 4. Rebuild SO in minikube (only if SO changes detected)

```bash
# From the repository root
./scripts/build-to-minikube.sh --deploy
```

This builds the Docker image inside minikube's docker environment and restarts all operator pods. Wait for the script to report success before proceeding.

If the deploy times out, check pod status:
```bash
kubectl --context minikube -n spark get pods
kubectl --context minikube -n spark describe pod <failing-pod>
```

### 5. Ensure Spark is deployed and running

Check if the Spark environment is already up:
```bash
kubectl --context minikube -n spark get pods
```

If pods are not running or healthy, start the environment:
```bash
./scripts/local-test.sh --dev-spark
```

**Note:** `local-test.sh` blocks (it keeps port-forwarding alive). If the environment is already running from a previous invocation, skip this step. If it needs to be started, run it in the background or instruct the user to run it in a separate terminal.

### 6. Run targeted integration tests

**Always prefer running specific tests over full suites.** Integration tests are slow; targeting specific tests saves significant time.

**Infer the package path from the test name.** Do not require the user to provide it. Search for the test function in the codebase:
```bash
grep -r "func TestName" spark/so/grpc_test/ --include='*_test.go' -l
```
Use the directory of the matching file as the package path.

**Run from the `spark/` directory:**

```bash
# Specific test (preferred - fastest)
cd spark && MINIKUBE_IP=$(minikube ip) go test -v -run TestMintTokens ./so/grpc_test/tokens_test/...

# Multiple specific tests
cd spark && MINIKUBE_IP=$(minikube ip) go test -v -run "TestMint|TestCreate" ./so/grpc_test/tokens_test/...

# All integration tests (slow - avoid unless needed)
cd spark && MINIKUBE_IP=$(minikube ip) go test -v -p 1 -tags=lightspark ./so/grpc_test/... ./so/grpc_test_internal/...
```

**Key flags:**
- `-v` for verbose output (always include)
- `-run <pattern>` to target specific test functions (always prefer this)
- `-p 1` to run packages sequentially (required for full suite, tests share state)
- `-tags=lightspark` for Lightspark-specific features
- `-count=1` to disable test caching if re-running after a rebuild
- `-timeout 10m` to extend timeout for longer tests

**Speed recommendations:**
- If the user mentions a specific bug or feature area, identify the relevant test(s) by name and run only those
- Use `-run TestName` regex patterns to narrow scope (e.g., `-run "TestMint|TestCreate"`)
- Only run the specific package that contains the relevant tests (e.g., `./so/grpc_test/tokens_test/...` not `./so/grpc_test/...`)
- Suggest `all` only as a final validation step before committing

### 7. If tests fail: Add debug logging

When a test fails and the cause isn't obvious from the test output, add temporary debug logging to the SO handler code.

**Logging pattern (Go with Zap):**
```go
// Get the logger from context
logger := logging.GetLoggerFromContext(ctx)

// Structured logging with fields
logger.Info("debug: handler reached checkpoint",
    zap.String("transfer_id", transferID),
    zap.Int("leaf_count", len(leaves)),
    zap.Error(err),
)

// Quick format-style logging
logger.Sugar().Infof("debug: value is %v", someValue)
```

**After adding logging:**
1. Rebuild: `./scripts/build-to-minikube.sh --deploy`
2. Re-run the failing test
3. Collect logs (see next section)
4. **Remove debug logging before committing**

### 8. Collecting SO logs from minikube

**Tail logs from a specific operator (most useful):**
```bash
# Operator 0 (coordinator)
kubectl --context minikube -n spark logs -f regtest-spark-dkg-0 -c operator

# Operator 1
kubectl --context minikube -n spark logs -f regtest-spark-dkg-1 -c operator

# Operator 2
kubectl --context minikube -n spark logs -f regtest-spark-dkg-2 -c operator
```

**Tail logs from RPC deployments:**
```bash
kubectl --context minikube -n spark logs -f deployment/regtest-spark-rpc-0 -c operator
kubectl --context minikube -n spark logs -f deployment/regtest-spark-rpc-1 -c operator
kubectl --context minikube -n spark logs -f deployment/regtest-spark-rpc-2 -c operator
```

**Search recent logs for errors:**
```bash
kubectl --context minikube -n spark logs regtest-spark-dkg-0 -c operator --since=5m | grep -i error
```

**Get logs from all operators at once:**
```bash
for i in 0 1 2; do
  echo "=== Operator $i ==="
  kubectl --context minikube -n spark logs regtest-spark-dkg-$i -c operator --tail=50
done
```

**Signer container logs (for FROST/crypto issues):**
```bash
kubectl --context minikube -n spark logs regtest-spark-dkg-0 -c signer
```

## Workflow Summary

```
1. /debug-spark-integration-tests [TestName]
   a. Checks minikube is running
   b. If no test specified, pulls failing tests from PR CI
   c. Detects SO changes -> rebuilds minikube image if needed
   d. Verifies Spark environment is running
   e. Runs targeted test(s)
2. If test fails:
   a. Check test output for obvious errors
   b. Collect SO logs from minikube pods
   c. Add debug logging if needed -> rebuild -> re-run
   d. Fix the issue -> rebuild -> re-run
3. Remove any temporary debug logging
4. Run test one final time to confirm fix
```

## Error Handling

- **minikube not running:** Tell user to run `minikube delete && USE_LIGHTSPARK_HELM_REPO=true minikube/setup.sh`
- **Build fails:** Show Docker build output; likely a compilation error in SO code
- **Pods crash after deploy:** Run `kubectl --context minikube -n spark describe pod <pod>` and check logs
- **Tests can't connect:** Verify `minikube ip` returns a valid IP and pods are Ready
- **Tests timeout:** Increase with `-timeout 15m`; check if pods restarted mid-test
- **No PR found:** Ask user for specific test names to run

## Important Notes

- Always prefer `-run TestSpecificName` over running entire packages
- Always infer the package path from the test name - don't ask the user for it
- The rebuild step (`build-to-minikube.sh --deploy`) takes ~1-2 minutes; skip it when only test files changed
- Use `-count=1` after rebuilds to bypass Go's test cache
- Debug logging is temporary; always clean it up before committing
- The `local-test.sh --dev-spark` script must remain running in a terminal for the environment to stay up
