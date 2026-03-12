# Token Transaction Integration Test Gaps

## Context
Identified gaps in the existing token transaction integration test suite at `spark/so/grpc_test/tokens_test/`. The tests run across TTV2, TTV3 Phase1, and TTV3 Phase2 modes. Several handler validation paths, query behaviors, and authorization checks have no corresponding integration tests.

## Approach
Add new tests to two existing test files (`validation_test.go`, `query_test.go`) and create one new file (`freeze_auth_test.go`) for freeze authorization. Tests follow existing patterns: use `runSignatureTypeTestCases`, table-driven where applicable, and `setupNativeTokenWithMint` for setup.

## Changes

### 1. `spark/so/grpc_test/tokens_test/validation_test.go`
- **What**: Add `TestBroadcastTokenTransactionV3ExecuteBeforeValidation` — V3-only table-driven test covering:
  - Valid `execute_before` within window succeeds
  - `execute_before` in the past is rejected
  - `execute_before` beyond `TokenMaxExecuteBeforeWindow` is rejected
  - `execute_before` before `client_created_timestamp` is rejected
  - `execute_before` with sub-microsecond precision is rejected
- **Why**: The `execute_before` field on `PartialTokenTransaction` is validated in `broadcastTokenTransactionPhase2` but has zero integration test coverage.

### 2. `spark/so/grpc_test/tokens_test/query_test.go`
- **What**: Add three new test functions:
  - `TestQueryTokenOutputsBackwardPaginationRejected` — verifies that `Direction_PREVIOUS` returns an error
  - `TestQueryTokenOutputsFilterCountLimits` — verifies that >500 entries in `owner_public_keys`, `issuer_public_keys`, or `token_identifiers` returns an error
  - `TestQueryTokenOutputsByTokenIdentifierOnly` — queries outputs using only `token_identifiers` (no owner/issuer keys) and verifies correct results
  - `TestQueryTokenTransactionsConfirmationMetadata` — mints, transfers, then queries and verifies `confirmation_metadata.spent_token_outputs_metadata` contains output IDs and revocation secrets
- **Why**: These cover untested query handler paths: backward pagination rejection, filter validation limits, standalone token identifier queries, and confirmation metadata population.

### 3. `spark/so/grpc_test/tokens_test/freeze_auth_test.go` (new)
- **What**: Add `TestFreezeTokensByNonIssuerFails` — creates a token, then attempts to freeze from a different identity key (not the issuer), verifying the auth check rejects it.
- **Why**: The `authz.EnforceSessionIdentityPublicKeyMatches` check in `freeze_token_handler.go` has no integration test.

## Verification
- [ ] `mise test-grpc` or `go test ./so/grpc_test/tokens_test/... -run TestBroadcastTokenTransactionV3ExecuteBeforeValidation`
- [ ] `go test ./so/grpc_test/tokens_test/... -run TestQueryTokenOutputsBackwardPaginationRejected`
- [ ] `go test ./so/grpc_test/tokens_test/... -run TestQueryTokenOutputsFilterCountLimits`
- [ ] `go test ./so/grpc_test/tokens_test/... -run TestQueryTokenOutputsByTokenIdentifierOnly`
- [ ] `go test ./so/grpc_test/tokens_test/... -run TestQueryTokenTransactionsConfirmationMetadata`
- [ ] `go test ./so/grpc_test/tokens_test/... -run TestFreezeTokensByNonIssuerFails`
- [ ] `mise lint` passes

## Risks
- Integration tests require a running local environment (minikube). Tests will be validated against the environment.
- `execute_before` tests only run in V3 Phase2 mode since that's the only code path that validates the field.
