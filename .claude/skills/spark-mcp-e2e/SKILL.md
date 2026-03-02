---
name: spark-mcp-e2e
user_invocable: true
---

# Spark MCP E2E Validation & Debugging

The Spark MCP server (`sdks/js/packages/spark-mcp/`) exposes wallet operations as tools for end-to-end validation of features, debugging flows against a running local Spark environment, and developing new MCP tools.

## Setup Verification

Before running any e2e workflow, verify the MCP tools are available by invoking any `spark_` tool (e.g., `spark_get_balance`).

**If tools are available**, proceed to the relevant workflow below.

**If tools are NOT available:**

1. Build the JS packages — the MCP server runs from compiled output:
   ```bash
   mise build-js-packages
   ```
2. Check for `.mcp.json` at the repo root. If missing, direct the user to the setup instructions in [`sdks/js/packages/spark-mcp/README.md`](../../../sdks/js/packages/spark-mcp/README.md) (Installation section).
3. If `.mcp.json` exists but tools still fail, consult the environment variable table in the README and the network routing details in [`sdks/js/packages/spark-mcp/CLAUDE.md`](../../../sdks/js/packages/spark-mcp/CLAUDE.md).
4. After any configuration change, a Claude session restart is required for MCP changes to take effect.

The README.md and CLAUDE.md in the spark-mcp package are the source of truth for setup and configuration — do not duplicate their contents here.

## Discovering Available Tools

Tools change as the MCP server evolves. Do not rely on a hardcoded tool list.

- **Preferred:** Invoke the MCP server directly — tools are prefixed `spark_`. Call any tool or use tool discovery to see what's available.
- **Fallback:** If the MCP server is not running, read the tool registrations in `sdks/js/packages/spark-mcp/src/tools/index.ts`.

## E2E Validation Workflows

After implementing a feature, use these patterns to verify it works against a running local Spark environment. Adapt steps to the specific feature under test.

### Transfers (Spark-to-Spark)

1. Create two wallets (sender and receiver)
2. Fund the sender wallet via the deposit flow
3. Send a transfer from sender to receiver
4. Verify: sender balance decreased, receiver balance increased
5. Verify: transfer appears in both wallets' transfer lists with correct status

### Deposits (Bitcoin L1 → Spark)

1. Create a wallet
2. Get a deposit address
3. Fund the address (LOCAL/REGTEST — uses local bitcoind)
4. Claim the deposit
5. Verify: wallet balance reflects the deposited amount
6. Deposit addresses are single-use — get a fresh address for each deposit
7. After claiming, balance may take a few seconds to propagate. Retry balance check if stale.

### Lightning Network

1. Create two wallets and fund at least one
2. Create an invoice on the receiving wallet
3. Estimate the fee on the sending wallet before paying
4. Pay the invoice from the sending wallet
5. Verify: receiver balance increased, sender balance decreased by amount + fee

### Withdrawals (Spark → Bitcoin L1)

1. Create and fund a wallet
2. Get a fee quote for withdrawal
3. Execute the withdrawal to a Bitcoin address
4. Verify: wallet balance decreased by withdrawal amount + fees
5. On LOCAL/REGTEST, mine a block and verify the on-chain transaction

### Smoke Test (Environment Health Check)

Quick check that the local Spark environment is functional:

1. Create a wallet — confirms SO connectivity
2. Fund via deposit flow — confirms bitcoind + chain watcher + deposit claiming
3. Create a second wallet, send a small transfer — confirms transfer path
4. Check balances on both wallets — confirms balance tracking

If any step fails, the error message usually indicates which component is down (SO, bitcoind, chain watcher, etc.).

## Debugging Flows

When debugging a specific issue:

1. **Reproduce with MCP tools** — use the relevant workflow above to isolate whether the problem is in the SDK, SO, or SSP layer.
2. **Check wallet state** — inspect balance and transfer lists to see current state.
3. **Compare expected vs actual** — if a transfer shows unexpected status, check transfer details for error information.
4. **Cross-reference logs** — pair MCP tool output with OpenSearch logs (via the `opensearch-logs` skill if available) to correlate wallet-level behavior with server-side processing.
5. **Test incrementally** — when fixing a bug, re-run the specific failing step rather than the entire flow.

## Proof of Work for PRs

When e2e validation was performed during the current session, capture the results for the PR description. Include a `## Proof of Work` section with:

1. A numbered summary of steps taken and results observed
2. Raw tool outputs in a collapsed `<details>` block

Example format:
```markdown
## Proof of Work

Validated against local Spark environment:

1. Created sender and receiver wallets
2. Funded sender with 50,000 sats via deposit flow
3. Sent transfer with new `memo` field set to `"test-memo"` → transfer ID `abc123`
4. Retrieved transfer on receiver — confirmed `memo: "test-memo"` present in response

<details>
<summary>MCP tool outputs</summary>

`spark_send_transfer` response:
...

`spark_get_transfer` response:
...

</details>
```

Only include proof when validation was actually performed — do not fabricate results.

## Developing New MCP Tools

When building a feature in the Spark SDK or SO that should be testable via MCP:

1. **Check existing tools.** Read `sdks/js/packages/spark-mcp/src/tools/index.ts` for current registrations — an existing tool may already cover the feature.
2. **Follow the existing pattern.** See [`sdks/js/packages/spark-mcp/CLAUDE.md`](../../../sdks/js/packages/spark-mcp/CLAUDE.md) for the package structure and "Adding a new tool" steps.
3. **Key conventions:**
   - Tool handlers accept an optional `resolve` parameter for dependency injection (enables testing with mock wallets)
   - Use Zod schemas for input validation
   - Return user-friendly error messages with recovery suggestions
   - Gate dev-only tools (funding, combined flows) on `BITCOIN_NETWORK` being `LOCAL`
4. **Write tests** using the mock wallet pattern — see existing tests in `sdks/js/packages/spark-mcp/src/tests/`.
5. **Rebuild** after changes: `mise build-js-packages`
6. **Restart Claude session** to pick up new tools, then validate the new tool end-to-end.
