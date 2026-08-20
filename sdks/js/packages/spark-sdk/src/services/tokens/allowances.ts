import {
  bytesToHex,
  hexToBytes,
  bytesToNumberBE,
  numberToBytesBE,
} from "@noble/curves/utils";
import { uuidv7obj } from "uuidv7";
import {
  toAllowanceRequestError,
  tokenAllowanceError,
} from "../../errors/token-allowances.js";
import { SparkRequestError, SparkValidationError } from "../../errors/types.js";
import type {
  BroadcastTransactionResponse,
  OutputWithPreviousTransactionData,
  PartialTokenTransaction,
  RevokeTokenAllowancePayload,
  SignatureWithIndex,
  TokenAllowanceInfo,
  TokenAllowancePayload,
} from "../../proto/spark_token.js";
import {
  PartialTokenTransaction as PartialTokenTransactionCodec,
  TokenAllowanceStatus,
} from "../../proto/spark_token.js";
import { LoggingService } from "../../utils/logging-service.js";
import { type WalletConfigService } from "../config.js";
import { type ConnectionManager } from "../connection/connection.js";
import {
  hashCreateTokenAllowancePayload,
  hashRevokeTokenAllowancePayload,
} from "../../utils/token-allowance-hashing.js";
import { verifyAllowanceRecord } from "../../utils/token-allowance-verification.js";
import { getSparkTokenPrimitives } from "../../token-primitives-bindings/token-primitives-bindings.js";
import { hashFinalTokenTransaction } from "../../utils/token-hashing.js";
import {
  MAX_TOKEN_OUTPUTS_TX,
  TokenTransactionService,
  tokenTransactionCallOptions,
} from "./token-transactions.js";

// The only version the operators accept; there is no second statement layout.
const TOKEN_ALLOWANCE_CREATE_PAYLOAD_VERSION = 1;
const TOKEN_ALLOWANCE_REVOKE_PAYLOAD_VERSION = 1;
const UINT128_MAX = (1n << 128n) - 1n;

// How often and how long to retry the coordinated create/revoke RPC until
// every operator reports the allowance as applied. Replaying the identical
// signed payload is idempotent at every operator, so retrying is always safe.

// The SO clamps a larger requested page size down to 100.
const ALLOWANCE_QUERY_PAGE_SIZE = 100;

/**
 * Aggregate page cap for a single queryTokenAllowances call. An operator that
 * keeps advancing the offset would otherwise loop forever, each iteration
 * taking a fresh deadline and appending another page.
 */
export const ALLOWANCE_QUERY_MAX_PAGES = 20;

/**
 * At most one ACTIVE allowance may exist per owner, spender, and token: a
 * second create for the same triple is refused with ALREADY_ACTIVE until the
 * existing grant is revoked or exhausted. Owners are also capped on total
 * ACTIVE allowances (QUOTA_EXCEEDED).
 */
/**
 * How far the coordinator-supplied clock may diverge from the local one before the SDK refuses
 * to sign a timestamp with it.
 *
 * ServerTimeSync is populated from operator-supplied `Date` headers, so it is not a trustworthy
 * source for a value we are about to sign. The federation refuses a revoke whose timestamp
 * predates its grant's, so a grant signed far in the future can never be revoked - refusing to
 * sign is strictly better than signing something unrevocable. The bound is wider than the
 * federation's own +/-1 minute freshness window so honest skew never trips it.
 */
const MAX_SERVER_TIME_DIVERGENCE_MS = 2 * 60 * 1000;

/**
 * One spending ceiling. The federation accepts exactly one encoding per ceiling - a positive
 * amount with the flag unset, or the flag set with no amount - so the union makes the invalid
 * combinations unrepresentable rather than deferring them to a runtime rejection.
 *
 * Waiving a ceiling is owner-signed and bound into the statement hash; the federation still
 * meters every spend for observability.
 */
/**
 * Decodes an integrator-supplied hex field, reporting a malformed value as an SDK validation
 * error rather than letting the hex decoder throw something opaque.
 */
const ALLOWANCE_ID_BYTES = 16;
const PUBLIC_KEY_BYTES = 33;
const TOKEN_IDENTIFIER_BYTES = 32;

function hexField(
  name: string,
  value: string,
  expectedBytes?: number,
): Uint8Array {
  let decoded: Uint8Array;
  try {
    decoded = hexToBytes(value);
  } catch {
    throw new SparkValidationError(`${name} must be hex-encoded`, {
      field: name,
      value,
    });
  }
  if (expectedBytes !== undefined && decoded.length !== expectedBytes) {
    throw new SparkValidationError(`${name} must be ${expectedBytes} bytes`, {
      field: name,
      value,
      expected: `${expectedBytes} bytes`,
    });
  }
  return decoded;
}

export type AllowanceCeiling =
  | { unlimited: true; amount?: never }
  | { unlimited?: false; amount: bigint };

export interface CreateTokenAllowanceParams {
  /** Hex-encoded compressed secp256k1 public key (33 bytes) of the delegated spender. */
  spenderPublicKey: string;
  /** Hex-encoded token identifier (32 bytes). */
  tokenIdentifier: string;
  /** Ceiling on a single delegated transaction. */
  perTransaction: AllowanceCeiling;
  /** Ceiling on cumulative value over the allowance lifetime. */
  total: AllowanceCeiling;
  /** Expiry of the allowance; must be in the future. */
  expiryTime: Date;
  /** Hex-encoded recipient keys the spender may pay. Empty permits any recipient. */
  recipientAllowlist?: string[];
  /** Hex-encoded allowance ID (16 bytes). Randomly generated when omitted. */
  allowanceId?: string;
}

export interface TokenAllowanceResult {
  /** Hex-encoded allowance ID. */
  allowanceId: string;
  /** Hex-encoded identity keys of the operators that have applied the operation. */
  appliedOperatorPublicKeys: string[];
}

export interface CreateTokenAllowanceResult extends TokenAllowanceResult {
  allowancePayload: TokenAllowancePayload;
}

interface QueryTokenAllowancesFilterBase {
  /** Hex-encoded token identifier. */
  tokenIdentifier?: string;
  /** When false (default), only ACTIVE allowances are returned. */
  includeInactive?: boolean;
}

/**
 * At least one party key is required. When authorization is enforced the federation demands
 * that the session identity equal the owner or the spender filter, so a token-only or empty
 * filter is a guaranteed PermissionDenied rather than a broad query.
 */
export type QueryTokenAllowancesFilter = QueryTokenAllowancesFilterBase &
  (
    | { ownerPublicKey: string; spenderPublicKey?: string }
    | { ownerPublicKey?: string; spenderPublicKey: string }
  );

export interface AllowancePullOutput {
  /** Hex-encoded receiver identity public key. */
  receiverPublicKey: string;
  tokenAmount: bigint;
}

export interface StartAllowancePullParams {
  /** Hex-encoded allowance ID. */
  allowanceId: string;
  /** Hex-encoded owner whose outputs are being spent under the allowance. */
  ownerPublicKey: string;
  /** Hex-encoded token identifier (32 bytes). */
  tokenIdentifier: string;
  /**
   * Settlement outputs to create. A change output back to the owner is
   * appended automatically.
   */
  outputs: AllowancePullOutput[];
  /**
   * Owner outputs to spend. When omitted, the owner's outputs for the token
   * are fetched and selected automatically.
   */
  selectedOutputs?: OutputWithPreviousTransactionData[];
}

export interface PreparedAllowancePull {
  partialTokenTransaction: PartialTokenTransaction;
  partialTokenTransactionHash: Uint8Array;
  /** One allowance-arm signature per input, by input index. */
  signatures: SignatureWithIndex[];
  /** Hex-encoded allowance ID. */
  allowanceId: string;
}

export interface CommitAllowancePullResult {
  /** Hex-encoded final token transaction hash. */
  txId: string;
  response: BroadcastTransactionResponse;
}

/**
 * Client surface for Spark token allowances (delegated spending).
 *
 * Owners call createTokenAllowance/revokeTokenAllowance with their wallet
 * identity key. Spenders (merchants) authenticate as their OWN wallet
 * identity - the same key the allowance names as spender - and call
 * startAllowancePull/commitAllowancePull to pull funds from the owner's
 * outputs within the allowance limits. All policy checks in this service are
 * client-side preflight only; every SO independently enforces the allowance
 * at prepare, sign, and pre-reveal.
 */
export class TokenAllowanceService extends TokenTransactionService {
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
    logging = LoggingService.fromConfig(config),
  ) {
    super(config, connectionManager, logging, "TokenAllowanceService");
  }

  public async createTokenAllowance(
    params: CreateTokenAllowanceParams,
  ): Promise<CreateTokenAllowanceResult> {
    const ownerPublicKey = await this.config.signer.getIdentityPublicKey();
    // Decode the integrator-facing hex at the boundary; everything below this line is bytes.
    const allowanceId = params.allowanceId
      ? hexField("allowanceId", params.allowanceId, ALLOWANCE_ID_BYTES)
      : uuidv7obj().bytes;
    const spenderPublicKey = hexField(
      "spenderPublicKey",
      params.spenderPublicKey,
      PUBLIC_KEY_BYTES,
    );
    const tokenIdentifier = hexField(
      "tokenIdentifier",
      params.tokenIdentifier,
      TOKEN_IDENTIFIER_BYTES,
    );
    const recipientAllowlist = (params.recipientAllowlist ?? []).map((key, i) =>
      hexField(`recipientAllowlist[${i}]`, key, PUBLIC_KEY_BYTES),
    );

    this.validateCreateParams(params, spenderPublicKey, ownerPublicKey);

    const allowancePayload: TokenAllowancePayload = {
      version: TOKEN_ALLOWANCE_CREATE_PAYLOAD_VERSION,
      allowanceId,
      ownerPublicKey,
      spenderPublicKey,
      tokenIdentifier,
      perTransactionCap: uint128Bytes(
        "perTransaction.amount",
        params.perTransaction.amount ?? 0n,
      ),
      totalLimit: uint128Bytes("total.amount", params.total.amount ?? 0n),
      perTransactionUnlimited: params.perTransaction.unlimited ?? false,
      totalUnlimited: params.total.unlimited ?? false,
      recipientAllowlist,
      expiryTime: params.expiryTime,
      network: this.config.getNetworkProto(),
      ownerProvidedTimestamp: this.ownerProvidedTimestampMs(),
    };

    const ownerSignature = await this.signWithIdentityKey(
      hashCreateTokenAllowancePayload(allowancePayload),
    );

    // Creation runs through the operators' consensus engine, which is all-or-nothing: the
    // coordinator only returns success once every operator has committed the grant. There is
    // therefore no partial progress to poll for, unlike revocation's durable gossip.
    const client = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );
    try {
      await client.create_token_allowance(
        { allowancePayload, ownerSignature },
        tokenTransactionCallOptions(),
      );
    } catch (error) {
      throw toAllowanceRequestError(error, "create_token_allowance");
    }
    const appliedOperatorPublicKeys = Object.values(
      this.config.getSigningOperators(),
    ).map((operator) => hexToBytes(operator.identityPublicKey));

    return {
      allowanceId: bytesToHex(allowanceId),
      allowancePayload,
      appliedOperatorPublicKeys: appliedOperatorPublicKeys.map(bytesToHex),
    };
  }

  public async revokeTokenAllowance(
    allowanceIdHex: string,
  ): Promise<TokenAllowanceResult> {
    const ownerPublicKey = await this.config.signer.getIdentityPublicKey();
    const allowanceId = hexField(
      "allowanceId",
      allowanceIdHex,
      ALLOWANCE_ID_BYTES,
    );

    const revokeAllowancePayload: RevokeTokenAllowancePayload = {
      version: TOKEN_ALLOWANCE_REVOKE_PAYLOAD_VERSION,
      allowanceId,
      ownerPublicKey,
      ownerProvidedTimestamp: this.ownerProvidedTimestampMs(),
    };

    const ownerSignature = await this.signWithIdentityKey(
      hashRevokeTokenAllowancePayload(revokeAllowancePayload),
    );

    // A successful revoke is already durable: the operator commits the tombstone and its gossip
    // row in one transaction, and the retry task redelivers to any peer that has not acked. The
    // reported progress is therefore a snapshot of the immediate fan-out, not a postcondition to
    // enforce - retrying until every operator appears would neither prove anything (the list is
    // one coordinator's unsigned claim) nor be necessary (an incomplete list self-heals), while
    // throwing on it would report a committed revocation as a failure.
    const client = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );
    let appliedOperatorPublicKeys: Uint8Array[];
    try {
      const response = await client.revoke_token_allowance(
        { revokeAllowancePayload, ownerSignature },
        tokenTransactionCallOptions(),
      );
      appliedOperatorPublicKeys =
        response.allowanceProgress?.appliedOperatorPublicKeys ?? [];
    } catch (error) {
      throw toAllowanceRequestError(error, "revoke_token_allowance");
    }

    return {
      allowanceId: allowanceIdHex,
      appliedOperatorPublicKeys: appliedOperatorPublicKeys.map(bytesToHex),
    };
  }

  public async queryTokenAllowances(
    filter: QueryTokenAllowancesFilter,
  ): Promise<TokenAllowanceInfo[]> {
    const client = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    const allowances: TokenAllowanceInfo[] = [];
    let offset = 0;
    let exceededPageCap = false;
    try {
      for (let page = 0; ; page++) {
        const response = await client.query_token_allowances(
          {
            ownerPublicKey: filter.ownerPublicKey
              ? hexField(
                  "ownerPublicKey",
                  filter.ownerPublicKey,
                  PUBLIC_KEY_BYTES,
                )
              : undefined,
            spenderPublicKey: filter.spenderPublicKey
              ? hexField(
                  "spenderPublicKey",
                  filter.spenderPublicKey,
                  PUBLIC_KEY_BYTES,
                )
              : undefined,
            tokenIdentifier: filter.tokenIdentifier
              ? hexField(
                  "tokenIdentifier",
                  filter.tokenIdentifier,
                  TOKEN_IDENTIFIER_BYTES,
                )
              : undefined,
            includeInactive: filter.includeInactive ?? false,
            limit: ALLOWANCE_QUERY_PAGE_SIZE,
            offset,
          },
          tokenTransactionCallOptions(),
        );
        allowances.push(...response.allowances);
        // A -1 offset means no rows remain; the progress check bounds the
        // loop even if an operator ever returns a non-advancing offset.
        if (
          response.offset < 0 ||
          response.allowances.length < ALLOWANCE_QUERY_PAGE_SIZE ||
          response.offset <= offset
        ) {
          break;
        }
        if (page + 1 >= ALLOWANCE_QUERY_MAX_PAGES) {
          exceededPageCap = true;
          break;
        }
        offset = response.offset;
      }
    } catch (error) {
      throw toAllowanceRequestError(error, "query_token_allowances");
    }
    if (exceededPageCap) {
      throw new SparkValidationError(
        "too many token allowances match this filter; narrow it with a token identifier or party key",
        {
          field: "filter",
          pages: ALLOWANCE_QUERY_MAX_PAGES,
          returned: allowances.length,
        },
      );
    }
    // Proves the owner-signed terms only; status and spentAmount are unsigned.
    for (const record of allowances) {
      verifyAllowanceRecord(record);
    }
    return allowances;
  }

  /**
   * Builds and signs a delegated pull: a V3 token transfer spending the
   * owner's outputs, authorized by the spender's signature over the partial
   * transaction hash (the same hash owners sign at start) on every input via
   * the allowance_signature arm. Enforces the allowance policy client-side as
   * a preflight - change back to the owner is free, every other output is
   * metered, and recipients must be within the allowlist when one is set - so
   * misconfigured pulls fail fast with typed errors instead of a server
   * round-trip. The SOs re-enforce all of this authoritatively at prepare.
   */
  public async startAllowancePull(
    params: StartAllowancePullParams,
  ): Promise<PreparedAllowancePull> {
    const { outputs } = params;
    const allowanceId = hexField(
      "allowanceId",
      params.allowanceId,
      ALLOWANCE_ID_BYTES,
    );
    const ownerPublicKey = hexField(
      "ownerPublicKey",
      params.ownerPublicKey,
      PUBLIC_KEY_BYTES,
    );
    const tokenIdentifier = hexField(
      "tokenIdentifier",
      params.tokenIdentifier,
      TOKEN_IDENTIFIER_BYTES,
    );
    if (outputs.length === 0) {
      throw new SparkValidationError("No pull outputs provided", {
        field: "outputs",
      });
    }

    const spenderPublicKey = await this.config.signer.getIdentityPublicKey();
    const allowance = await this.loadAllowanceForPull(
      allowanceId,
      ownerPublicKey,
      tokenIdentifier,
      spenderPublicKey,
    );
    const payload = allowance.allowancePayload!;

    const tokenOutputData = this.buildMeteredOutputs(
      params,
      allowance,
      payload,
    );

    const totalCreated = tokenOutputData.reduce(
      (sum, output) => sum + output.tokenAmount,
      0n,
    );
    const selectedOutputs =
      params.selectedOutputs ??
      (await this.selectOwnerOutputs(
        ownerPublicKey,
        tokenIdentifier,
        totalCreated,
      ));

    this.validateSelectedOwnerOutputs(
      selectedOutputs,
      ownerPublicKey,
      tokenIdentifier,
    );

    const availableAmount = selectedOutputs.reduce(
      (sum, output) => sum + bytesToNumberBE(output.output!.tokenAmount),
      0n,
    );
    if (availableAmount > totalCreated) {
      tokenOutputData.push({
        receiverPublicKey: ownerPublicKey,
        rawTokenIdentifier: tokenIdentifier,
        tokenAmount: availableAmount - totalCreated,
      });
    } else if (availableAmount < totalCreated) {
      throw new SparkValidationError(
        "Selected owner outputs do not cover the pull amount",
        {
          field: "selectedOutputs",
          value: availableAmount,
          expected: totalCreated,
        },
      );
    }

    const partialTokenTransaction =
      await this.constructPartialTransferTokenTransaction(
        selectedOutputs,
        tokenOutputData,
      );

    // appendChange keys change to the signer, who is the spender on a pull.
    if (
      partialTokenTransaction.partialTokenOutputs.length !==
      tokenOutputData.length
    ) {
      throw new SparkValidationError(
        "allowance pull produced an unexpected output; change must be explicit",
        {
          field: "partialTokenOutputs",
          value: partialTokenTransaction.partialTokenOutputs.length,
          expected: tokenOutputData.length,
        },
      );
    }

    // The bindings constructor cannot express the allowance signature arm, so
    // construction stays legacy. TODO(SP-3858): migrate it once it can.
    const partialTokenTransactionHash =
      await getSparkTokenPrimitives().hashPartialTokenTransaction(
        PartialTokenTransactionCodec.encode(partialTokenTransaction).finish(),
      );
    const spenderSignature = await this.signWithIdentityKey(
      partialTokenTransactionHash,
    );

    const numInputs =
      partialTokenTransaction.tokenInputs?.$case === "transferInput"
        ? partialTokenTransaction.tokenInputs.transferInput.outputsToSpend
            .length
        : 0;
    if (numInputs === 0) {
      throw new SparkValidationError(
        "Allowance pull must spend at least one token input",
        { field: "selectedOutputs" },
      );
    }
    const signatures: SignatureWithIndex[] = [];
    for (let inputIndex = 0; inputIndex < numInputs; inputIndex++) {
      signatures.push({
        inputIndex,
        authoritySignatures: {
          $case: "allowanceSignature",
          allowanceSignature: {
            allowanceId,
            spenderSignature: {
              publicKey: spenderPublicKey,
              signature: spenderSignature,
            },
          },
        },
      });
    }

    return {
      partialTokenTransaction,
      partialTokenTransactionHash,
      signatures,
      allowanceId: params.allowanceId,
    };
  }

  /**
   * Submits a prepared pull. V3 commit requires no additional client
   * signatures (per-operator input signatures are only required for versions
   * below 3), so this reuses the existing V3 broadcast flow: the coordinator
   * starts the transaction across operators and commits it server-side. The
   * coordinator keys a broadcast on the partial transaction hash and replays
   * the original result for a resubmission, so a retry after an ambiguous
   * failure cannot spend the inputs twice.
   */
  public async commitAllowancePull(
    prepared: PreparedAllowancePull,
  ): Promise<CommitAllowancePullResult> {
    const client = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    let response: BroadcastTransactionResponse;
    try {
      response = await client.broadcast_transaction(
        {
          identityPublicKey: await this.config.signer.getIdentityPublicKey(),
          partialTokenTransaction: prepared.partialTokenTransaction,
          tokenTransactionOwnerSignatures: prepared.signatures,
        },
        tokenTransactionCallOptions(),
      );
    } catch (error) {
      throw toAllowanceRequestError(error, "broadcast_transaction");
    }

    if (!response.finalTokenTransaction) {
      throw new SparkRequestError(
        "Final token transaction missing in broadcast response",
        { operation: "broadcast_transaction" },
      );
    }

    const finalHash = await hashFinalTokenTransaction(
      response.finalTokenTransaction,
    );
    return { txId: bytesToHex(finalHash), response };
  }

  private async loadAllowanceForPull(
    allowanceId: Uint8Array,
    ownerPublicKey: Uint8Array,
    tokenIdentifier: Uint8Array,
    spenderPublicKey: Uint8Array,
  ): Promise<TokenAllowanceInfo> {
    const allowanceIdHex = bytesToHex(allowanceId);
    const matching = (records: TokenAllowanceInfo[]) =>
      records.find(
        (info) =>
          info.allowancePayload &&
          bytesToHex(info.allowancePayload.allowanceId) === allowanceIdHex,
      );

    // EXHAUSTED grants are absent from the ACTIVE query yet still spendable,
    // since lazy release can flip them back, so the wide scan stays a fallback.
    // The public query surface is hex; this internal resolver works in bytes.
    const filter = {
      ownerPublicKey: bytesToHex(ownerPublicKey),
      spenderPublicKey: bytesToHex(spenderPublicKey),
      tokenIdentifier: bytesToHex(tokenIdentifier),
    };
    const allowance =
      matching(await this.queryTokenAllowances(filter)) ??
      matching(
        await this.queryTokenAllowances({ ...filter, includeInactive: true }),
      );
    if (!allowance) {
      throw tokenAllowanceError("NOT_FOUND", "token allowance not found", {
        allowanceId: allowanceIdHex,
      });
    }
    // A queried operator cannot forge these terms without breaking the owner
    // signature, and the immutable fields must be the ones we asked about.
    verifyAllowanceRecord(allowance);
    const payload = allowance.allowancePayload!;
    for (const [field, queried, returned] of [
      ["ownerPublicKey", ownerPublicKey, payload.ownerPublicKey],
      ["spenderPublicKey", spenderPublicKey, payload.spenderPublicKey],
      ["tokenIdentifier", tokenIdentifier, payload.tokenIdentifier],
    ] as const) {
      if (bytesToHex(queried) !== bytesToHex(returned)) {
        throw new SparkValidationError(
          `returned allowance ${field} does not match the requested allowance`,
          { field, allowanceId: allowanceIdHex },
        );
      }
    }
    const payloadNetwork: number = payload.network;
    if (payloadNetwork !== this.config.getNetworkProto()) {
      throw new SparkValidationError(
        "returned allowance network does not match the wallet network",
        { field: "network", allowanceId: allowanceIdHex },
      );
    }
    if (
      allowance.status === TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_REVOKED
    ) {
      throw tokenAllowanceError("REVOKED", "token allowance has been revoked", {
        allowanceId: allowanceIdHex,
      });
    }
    const expiryTime = allowance.allowancePayload!.expiryTime;
    if (expiryTime && expiryTime.getTime() <= Date.now()) {
      throw tokenAllowanceError("EXPIRED", "token allowance has expired", {
        allowanceId: allowanceIdHex,
        expiryTime,
      });
    }
    return allowance;
  }

  /**
   * Computes the metered output set for a pull: the caller's settlement
   * outputs. Change back to the owner is never metered; every other output
   * counts toward the metered amount. Enforces the allowlist, per-transaction
   * cap, and remaining budget client-side.
   */
  private buildMeteredOutputs(
    params: StartAllowancePullParams,
    allowance: TokenAllowanceInfo,
    payload: TokenAllowancePayload,
  ): Array<{
    receiverPublicKey: Uint8Array;
    rawTokenIdentifier: Uint8Array;
    tokenAmount: bigint;
  }> {
    const ownerHex = bytesToHex(payload.ownerPublicKey);
    const allowlistHex = new Set(
      payload.recipientAllowlist.map((key) => bytesToHex(key)),
    );

    const receivers = params.outputs.map((output, i) =>
      hexField(
        `outputs[${i}].receiverPublicKey`,
        output.receiverPublicKey,
        PUBLIC_KEY_BYTES,
      ),
    );
    const rawTokenIdentifier = hexField(
      "tokenIdentifier",
      params.tokenIdentifier,
      TOKEN_IDENTIFIER_BYTES,
    );

    let settled = 0n;
    for (const [i, output] of params.outputs.entries()) {
      if (output.tokenAmount <= 0n) {
        throw new SparkValidationError(
          `Pull output at index ${i} must have a positive amount`,
          { field: `outputs[${i}].tokenAmount`, value: output.tokenAmount },
        );
      }
      const receiverHex = bytesToHex(receivers[i]!);
      if (receiverHex === ownerHex) {
        // Change back to the owner is never metered.
        continue;
      }
      if (allowlistHex.size > 0 && !allowlistHex.has(receiverHex)) {
        throw tokenAllowanceError(
          "RECIPIENT_NOT_ALLOWED",
          "output recipient is not in the allowance recipient allowlist",
          { field: `outputs[${i}]`, receiver: receiverHex },
        );
      }
      settled += output.tokenAmount;
    }

    const tokenOutputData = params.outputs.map((output, i) => ({
      receiverPublicKey: receivers[i]!,
      rawTokenIdentifier,
      tokenAmount: output.tokenAmount,
    }));

    // An owner-signed unlimited flag waives its ceiling; the SO still meters.
    const metered = settled;
    if (!payload.perTransactionUnlimited) {
      const perTransactionCap = bytesToNumberBE(payload.perTransactionCap);
      if (metered > perTransactionCap) {
        throw tokenAllowanceError(
          "PER_TRANSACTION_CAP_EXCEEDED",
          "metered amount exceeds the allowance per-transaction cap",
          { metered, perTransactionCap },
        );
      }
    }
    // spentAmount counts RESERVED spends the operators release inside prepare,
    // so rejecting here would block the call that frees the budget.
    if (!payload.totalUnlimited) {
      const totalLimit = bytesToNumberBE(payload.totalLimit);
      const spentAmount = bytesToNumberBE(allowance.spentAmount);
      if (spentAmount + metered > totalLimit) {
        this.logger.warn(
          `allowance ${bytesToHex(payload.allowanceId)} reports ${spentAmount} of ${totalLimit} spent;` +
            ` pulling ${metered} succeeds only if the operators release stale reservations`,
        );
      }
    }

    return tokenOutputData;
  }

  private validateSelectedOwnerOutputs(
    selectedOutputs: OutputWithPreviousTransactionData[],
    ownerPublicKey: Uint8Array,
    tokenIdentifier: Uint8Array,
  ): void {
    if (selectedOutputs.length > MAX_TOKEN_OUTPUTS_TX) {
      throw new SparkValidationError(
        `Cannot spend more than ${MAX_TOKEN_OUTPUTS_TX} token outputs in a single allowance pull`,
        { field: "selectedOutputs", value: selectedOutputs.length },
      );
    }
    const ownerHex = bytesToHex(ownerPublicKey);
    const tokenHex = bytesToHex(tokenIdentifier);
    const seen = new Set<string>();
    for (const [i, selected] of selectedOutputs.entries()) {
      const output = selected.output;
      if (!output?.id || !output.tokenIdentifier || !output.tokenAmount) {
        throw new SparkValidationError(
          `Selected output at index ${i} is missing output data`,
          { field: `selectedOutputs[${i}]` },
        );
      }
      if (seen.has(output.id)) {
        throw new SparkValidationError(
          `Selected output at index ${i} is a duplicate of an earlier output`,
          { field: `selectedOutputs[${i}]`, value: output.id },
        );
      }
      seen.add(output.id);
      if (bytesToHex(output.tokenIdentifier) !== tokenHex) {
        throw new SparkValidationError(
          `Selected output at index ${i} does not match the allowance token`,
          { field: `selectedOutputs[${i}].tokenIdentifier` },
        );
      }
      if (bytesToHex(output.ownerPublicKey) !== ownerHex) {
        throw new SparkValidationError(
          `Selected output at index ${i} is not owned by the allowance owner`,
          { field: `selectedOutputs[${i}].ownerPublicKey` },
        );
      }
    }
  }

  private async selectOwnerOutputs(
    ownerPublicKey: Uint8Array,
    tokenIdentifier: Uint8Array,
    totalAmount: bigint,
  ): Promise<OutputWithPreviousTransactionData[]> {
    const ownedOutputs = await this.fetchOwnedTokenOutputs({
      ownerPublicKeys: [ownerPublicKey],
      tokenIdentifiers: [tokenIdentifier],
    });
    return this.selectTokenOutputs(ownedOutputs, totalAmount, "SMALL_FIRST");
  }

  /**
   * Runs a coordinated allowance RPC until every signing operator reports the
   * operation as applied. Safe to retry because replaying the identical
   * signed payload is idempotent at every operator.
   */
  private validateCreateParams(
    params: CreateTokenAllowanceParams,
    spenderPublicKey: Uint8Array,
    ownerPublicKey: Uint8Array,
  ): void {
    // The AllowanceCeiling union already makes the invalid amount/unlimited combinations
    // unrepresentable, so only the value ranges and the cross-ceiling relation remain.
    const perTransactionCap = params.perTransaction.amount ?? 0n;
    const totalLimit = params.total.amount ?? 0n;
    if (!params.perTransaction.unlimited && perTransactionCap <= 0n) {
      throw new SparkValidationError(
        "perTransaction.amount must be greater than 0",
        { field: "perTransaction.amount", value: perTransactionCap },
      );
    }
    if (!params.total.unlimited && totalLimit <= 0n) {
      throw new SparkValidationError("total.amount must be greater than 0", {
        field: "total.amount",
        value: totalLimit,
      });
    }
    if (
      !params.perTransaction.unlimited &&
      !params.total.unlimited &&
      perTransactionCap > totalLimit
    ) {
      throw new SparkValidationError(
        "perTransaction.amount must not exceed total.amount",
        {
          field: "perTransaction.amount",
          value: perTransactionCap,
          expected: `<= ${totalLimit}`,
        },
      );
    }

    // Expiry is signed and enforced as whole Unix seconds, so a timestamp
    // later in the current second is already past once truncated.
    if (!Number.isFinite(params.expiryTime.getTime())) {
      throw new SparkValidationError("expiryTime must be a valid date", {
        field: "expiryTime",
        value: params.expiryTime,
      });
    }
    if (
      Math.floor(params.expiryTime.getTime() / 1000) <=
      Math.floor(Date.now() / 1000)
    ) {
      throw new SparkValidationError("expiryTime must be in the future", {
        field: "expiryTime",
        value: params.expiryTime,
      });
    }
    const ownerHex = bytesToHex(ownerPublicKey);
    if (bytesToHex(spenderPublicKey) === ownerHex) {
      throw new SparkValidationError(
        "spenderPublicKey must differ from the wallet identity (owner) key",
        { field: "spenderPublicKey" },
      );
    }
    for (const [i, key] of (params.recipientAllowlist ?? []).entries()) {
      if (key.toLowerCase() === ownerHex) {
        throw new SparkValidationError(
          "recipientAllowlist must not contain the owner key",
          { field: `recipientAllowlist[${i}]` },
        );
      }
    }
  }

  /**
   * The owner-signed ordering timestamp. Taken from server time when it agrees with the local
   * clock, so ordinary client skew is still absorbed, but never blindly: see
   * MAX_SERVER_TIME_DIVERGENCE_MS.
   */
  private ownerProvidedTimestampMs(): number {
    const localMs = Date.now();
    const serverMs = this.connectionManager.getCurrentServerTime()?.getTime();
    if (serverMs === undefined || Number.isNaN(serverMs)) {
      return localMs;
    }
    if (Math.abs(serverMs - localMs) > MAX_SERVER_TIME_DIVERGENCE_MS) {
      throw new SparkValidationError(
        "Coordinator-reported time diverges from local time; refusing to sign an allowance timestamp",
        {
          field: "ownerProvidedTimestamp",
          value: serverMs,
          expected: `within ${MAX_SERVER_TIME_DIVERGENCE_MS}ms of ${localMs}`,
        },
      );
    }
    return serverMs;
  }

  private async signWithIdentityKey(message: Uint8Array): Promise<Uint8Array> {
    if (this.config.getTokenSignatures() === "SCHNORR") {
      return await this.config.signer.signSchnorrWithIdentityKey(message);
    }
    return await this.config.signer.signMessageWithIdentityKey(message);
  }
}

function uint128Bytes(name: string, value: bigint): Uint8Array {
  if (value < 0n || value > UINT128_MAX) {
    throw new SparkValidationError(
      `${name} must fit in an unsigned 128-bit integer`,
      {
        field: name,
        value,
      },
    );
  }
  return numberToBytesBE(value, 16);
}
