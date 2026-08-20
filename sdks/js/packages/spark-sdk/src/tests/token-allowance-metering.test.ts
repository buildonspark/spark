import { hexToBytes } from "@noble/hashes/utils";
import { tokenAllowanceFailureOf } from "../errors/token-allowances.js";
import { Network } from "../proto/spark.js";
import type {
  TokenAllowanceInfo,
  TokenAllowancePayload,
} from "../proto/spark_token.js";
import { TokenAllowanceStatus } from "../proto/spark_token.js";
import {
  TokenAllowanceService,
  type StartAllowancePullParams,
} from "../services/tokens/allowances.js";

// Mirrors the constants in token-allowance-hashing.test.ts.
const OWNER_KEY_HEX =
  "02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe";
const SPENDER_KEY_HEX =
  "033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5";
const RECIPIENT_HEX =
  "0375a9121cd7c3684ca1941978cc0dc42ce316fddf70261643f17ba3eeca6d10f2";
const ALLOWANCE_ID_HEX = "0123456789abcdef0123456789abcdef";
const TOKEN_ID_HEX =
  "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451";
const PER_TX_CAP_HEX = "00000000000000000000000000002710"; // 10000
const TOTAL_LIMIT_HEX = "000000000000000000000000000186a0"; // 100000
const ZERO_U128_HEX = "00000000000000000000000000000000";

// buildMeteredOutputs is a private method that deliberately reads no instance
// state, so the metering math is unit-testable without constructing a wallet.
type MeteredOutput = {
  receiverPublicKey: Uint8Array;
  rawTokenIdentifier: Uint8Array;
  tokenAmount: bigint;
};
const buildMeteredOutputs: (
  params: StartAllowancePullParams,
  allowance: TokenAllowanceInfo,
  payload: TokenAllowancePayload,
) => MeteredOutput[] = (
  TokenAllowanceService.prototype as unknown as {
    buildMeteredOutputs: (
      params: StartAllowancePullParams,
      allowance: TokenAllowanceInfo,
      payload: TokenAllowancePayload,
    ) => MeteredOutput[];
  }
).buildMeteredOutputs;

function basePayload(
  overrides: Partial<TokenAllowancePayload> = {},
): TokenAllowancePayload {
  return {
    version: 1,
    allowanceId: hexToBytes(ALLOWANCE_ID_HEX),
    ownerPublicKey: hexToBytes(OWNER_KEY_HEX),
    spenderPublicKey: hexToBytes(SPENDER_KEY_HEX),
    tokenIdentifier: hexToBytes(TOKEN_ID_HEX),
    perTransactionCap: hexToBytes(PER_TX_CAP_HEX),
    totalLimit: hexToBytes(TOTAL_LIMIT_HEX),
    perTransactionUnlimited: false,
    totalUnlimited: false,
    recipientAllowlist: [hexToBytes(RECIPIENT_HEX)],
    expiryTime: new Date(2000000000 * 1000),
    network: Network.REGTEST,
    ownerProvidedTimestamp: 1747337980820,
    ...overrides,
  };
}

function allowanceWith(
  payload: TokenAllowancePayload,
  spentAmountHex: string = ZERO_U128_HEX,
): TokenAllowanceInfo {
  return {
    allowancePayload: payload,
    spentAmount: hexToBytes(spentAmountHex),
    status: TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_ACTIVE,
    ownerSignature: new Uint8Array(0),
    revokeSignature: new Uint8Array(0),
    ownerProvidedRevokeTimestamp: 0,
    revokeVersion: 0,
  };
}

function pullParams(
  outputs: Array<{ receiverPublicKey: string; tokenAmount: bigint }>,
): StartAllowancePullParams {
  return {
    allowanceId: ALLOWANCE_ID_HEX,
    ownerPublicKey: OWNER_KEY_HEX,
    tokenIdentifier: TOKEN_ID_HEX,
    outputs,
  };
}

function captureThrow(run: () => unknown): unknown {
  try {
    run();
  } catch (error) {
    return error;
  }
  throw new Error("expected the call to throw");
}

describe("buildMeteredOutputs metering", () => {
  it("meters settled recipient outputs without appending anything", () => {
    const payload = basePayload();
    const outputs = buildMeteredOutputs(
      pullParams([{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 1000n }]),
      allowanceWith(payload),
      payload,
    );

    // The single settlement output, unchanged; metered == settled.
    expect(outputs).toHaveLength(1);
    expect(outputs[0]!.receiverPublicKey).toEqual(hexToBytes(RECIPIENT_HEX));
    expect(outputs[0]!.tokenAmount).toBe(1000n);
  });

  it("exempts an owner output larger than the per-transaction cap", () => {
    const payload = basePayload();
    const outputs = buildMeteredOutputs(
      pullParams([{ receiverPublicKey: OWNER_KEY_HEX, tokenAmount: 50000n }]),
      allowanceWith(payload),
      payload,
    );

    expect(outputs).toHaveLength(1);
    expect(outputs[0]!.receiverPublicKey).toEqual(hexToBytes(OWNER_KEY_HEX));
    expect(outputs[0]!.tokenAmount).toBe(50000n);
  });

  it("exempts an owner output that would exhaust the remaining budget", () => {
    const payload = basePayload();
    const outputs = buildMeteredOutputs(
      pullParams([{ receiverPublicKey: OWNER_KEY_HEX, tokenAmount: 50000n }]),
      allowanceWith(payload, "00000000000000000000000000017700"),
      payload,
    );

    expect(outputs).toHaveLength(1);
    expect(outputs[0]!.tokenAmount).toBe(50000n);
  });

  it("meters only the non-owner share of a mixed output set", () => {
    const payload = basePayload();
    const outputs = buildMeteredOutputs(
      pullParams([
        { receiverPublicKey: RECIPIENT_HEX, tokenAmount: 9000n },
        { receiverPublicKey: OWNER_KEY_HEX, tokenAmount: 50000n },
      ]),
      allowanceWith(payload),
      payload,
    );

    expect(outputs).toHaveLength(2);
    expect(outputs.map((output) => output.tokenAmount)).toEqual([
      9000n,
      50000n,
    ]);
  });

  it("rejects when the settled amount exceeds the per-transaction cap", () => {
    const payload = basePayload();
    // settled=10001 > cap 10000.
    const thrown = captureThrow(() =>
      buildMeteredOutputs(
        pullParams([{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 10001n }]),
        allowanceWith(payload),
        payload,
      ),
    );

    expect(tokenAllowanceFailureOf(thrown)).toBe(
      "PER_TRANSACTION_CAP_EXCEEDED",
    );
  });
});
