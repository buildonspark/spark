import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes } from "@noble/hashes/utils";
import { SparkValidationError } from "../errors/types.js";
import { Network } from "../proto/spark.js";
import type {
  TokenAllowanceInfo,
  TokenAllowancePayload,
} from "../proto/spark_token.js";
import { TokenAllowanceStatus } from "../proto/spark_token.js";
import {
  hashCreateTokenAllowancePayload,
  hashRevokeTokenAllowancePayload,
} from "../utils/token-allowance-hashing.js";
import { verifyAllowanceRecord } from "../utils/token-allowance-verification.js";

// The payload template mirrors the frozen-vector constants in
// token-allowance-hashing.test.ts; only the owner key is swapped for a
// generated one so the test can produce real signatures.
const SPENDER_KEY_HEX =
  "033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5";
const ALLOWANCE_ID_HEX = "0123456789abcdef0123456789abcdef";
const TOKEN_ID_HEX =
  "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451";

const ownerPrivateKey = secp256k1.utils.randomPrivateKey();
const ownerPublicKey = secp256k1.getPublicKey(ownerPrivateKey, true);

function ownerPayload(): TokenAllowancePayload {
  return {
    version: 1,
    allowanceId: hexToBytes(ALLOWANCE_ID_HEX),
    ownerPublicKey,
    spenderPublicKey: hexToBytes(SPENDER_KEY_HEX),
    tokenIdentifier: hexToBytes(TOKEN_ID_HEX),
    perTransactionCap: hexToBytes("00000000000000000000000000002710"),
    totalLimit: hexToBytes("000000000000000000000000000186a0"),
    perTransactionUnlimited: false,
    totalUnlimited: false,
    recipientAllowlist: [],
    expiryTime: new Date(2000000000 * 1000),
    network: Network.REGTEST,
    ownerProvidedTimestamp: 1747337980820,
  };
}

function signedRecord(
  sign: (hash: Uint8Array) => Uint8Array,
): TokenAllowanceInfo {
  const payload = ownerPayload();
  return {
    allowancePayload: payload,
    spentAmount: new Uint8Array(16),
    status: TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_ACTIVE,
    ownerSignature: sign(hashCreateTokenAllowancePayload(payload)),
    revokeSignature: new Uint8Array(0),
    ownerProvidedRevokeTimestamp: 0,
    revokeVersion: 0,
  };
}

const signEcdsaDer = (hash: Uint8Array): Uint8Array =>
  secp256k1.sign(hash, ownerPrivateKey).toDERRawBytes();
const signSchnorr = (hash: Uint8Array): Uint8Array =>
  schnorr.sign(hash, ownerPrivateKey);
const signEcdsaDerHighS = (hash: Uint8Array): Uint8Array => {
  const sig = secp256k1.sign(hash, ownerPrivateKey, { lowS: true });
  return new secp256k1.Signature(
    sig.r,
    secp256k1.CURVE.n - sig.s,
  ).toDERRawBytes();
};

describe("verifyAllowanceRecord", () => {
  it("accepts a high-S DER owner signature, as every SO does", () => {
    expect(() =>
      verifyAllowanceRecord(signedRecord(signEcdsaDerHighS)),
    ).not.toThrow();
  });

  it("accepts a record whose ECDSA DER owner signature matches the terms", () => {
    expect(() =>
      verifyAllowanceRecord(signedRecord(signEcdsaDer)),
    ).not.toThrow();
  });

  it("accepts a record whose Schnorr owner signature matches the terms", () => {
    expect(() =>
      verifyAllowanceRecord(signedRecord(signSchnorr)),
    ).not.toThrow();
  });

  it("rejects a record whose policy terms were altered after signing", () => {
    const record = signedRecord(signEcdsaDer);
    record.allowancePayload!.perTransactionCap = hexToBytes(
      "00000000000000000000000000002711",
    );
    expect(() => verifyAllowanceRecord(record)).toThrow(SparkValidationError);
  });

  it("verifies the revoke proof on REVOKED records and rejects forgeries", () => {
    const record = signedRecord(signEcdsaDer);
    const revokeTimestamp = 1747338980820;
    record.status = TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_REVOKED;
    record.revokeVersion = 1;
    record.ownerProvidedRevokeTimestamp = revokeTimestamp;
    record.revokeSignature = signEcdsaDer(
      hashRevokeTokenAllowancePayload({
        version: 1,
        allowanceId: record.allowancePayload!.allowanceId,
        ownerPublicKey,
        ownerProvidedTimestamp: revokeTimestamp,
      }),
    );
    expect(() => verifyAllowanceRecord(record)).not.toThrow();

    // A forged revoke timestamp breaks the proof.
    record.ownerProvidedRevokeTimestamp += 1;
    expect(() => verifyAllowanceRecord(record)).toThrow(SparkValidationError);
  });
});
