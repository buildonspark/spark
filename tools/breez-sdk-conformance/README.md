# Breez SDK Spark conformance tests

Runs the **released** `breez-sdk-spark` Python package (installed from PyPI) against
Lightspark's regtest Spark stack, using only the public SDK API. The point is to validate
exactly what an integrator consumes, so regressions in a Breez release are caught before we
adopt it. This replaces maintaining a private SDK fork just to host integration tests.

Scope is **tokens** (BTKN): we consume the Breez SDK Spark for token issuance and transfer,
so the suite exercises the token surface end to end. Bitcoin/Lightning flows are out of scope.

Each test issues and mints its own token. A fresh regtest wallet can create and mint a token
with no on-chain funding, so the suite is self-contained: no faucet, no credentials, no secrets.

## Layout

- `breez_conformance/config.py` — builds the regtest `Config`. Defaults to the released SDK's
  regtest operators/SSP; set `BREEZ_CONFORMANCE_STACK=loadtest` to target Lightspark's loadtest
  operators instead.
- `breez_conformance/wallet.py` — SDK lifecycle: `build_sdk()` (fresh wallet per test),
  event-queue listening, `new_spark_address`, and the generic polling/wait machinery.
- `breez_conformance/tokens.py` — token operations: `create_and_mint_token`, `send_tokens`,
  `new_token_invoice`, `token_balance`, and `wait_for_token_balance` (token mints/transfers
  settle asynchronously).
- `conftest.py` — `alice_sdk` / `bob_sdk` fixtures (fresh wallet per test) and a `funded_token`
  fixture (a token issued + minted by `alice_sdk`, settled and ready to transfer).
- `tests/` — the token conformance tests.

## Running locally

```bash
cd tools/breez-sdk-conformance
uv venv && source .venv/bin/activate
uv pip install -e ".[test]"
uv pip install "breez-sdk-spark==0.17.1"   # the released version under test

pytest
```

All tests need network reachability to the Spark regtest operators (the SDK syncs on connect).

## CI

`.github/workflows/breez-sdk-conformance.yaml` runs the suite on `workflow_dispatch` (with a
`sdk_version` input) and nightly. It pins the SDK version and runs `pytest`. No secrets required.

## Scope

Covered token flows: create + mint, metadata retrieval, transfer by spark address, transfer by
token spark invoice, burn, freeze/unfreeze, and token invoice/address parsing. Bitcoin/Lightning,
deposits/withdrawals, recovery, and LNURL are intentionally out of scope. Rust-internal tests
(storage concurrency, distributed lock, real-time sync, external signer) are also out of scope —
they exercise SDK internals not exposed by the released package.
