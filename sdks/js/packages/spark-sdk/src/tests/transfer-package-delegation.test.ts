import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";

import { HashVariant, type TransferPackage } from "../proto/spark.js";
import { getTransferPackageSigningPayload } from "../utils/transfer_package.js";

// The Go signer is the reference. These mirror
// spark/common/delegation_statement_vectors_test.go and
// spark/common/transfer_package_test.go: if the two implementations drift, a
// package signed by this SDK stops verifying against the operators.

const TRANSFER_ID = "44444444-4444-4444-4444-444444444444";

/** Mirrors vectorPubkey(b) in the Go vectors test: 33 bytes, 0x02 then b last. */
function vectorPubkey(b: number): Uint8Array {
  const pk = new Uint8Array(33);
  pk[0] = 0x02;
  pk[32] = b;
  return pk;
}

function basePackage(): TransferPackage {
  return {
    leavesToSend: [],
    keyTweakPackage: {
      "1": new Uint8Array([0xde, 0xad]),
      "2": new Uint8Array([0xbe, 0xef]),
    },
    userSignature: new Uint8Array(),
    directLeavesToSend: [],
    directFromCpfpLeavesToSend: [],
    hashVariant: HashVariant.HASH_VARIANT_V2,
    delegationIntent: undefined,
  };
}

describe("transfer package signing payload: delegation intent", () => {
  // Frozen vector from TestDelegationStatementFrozenVectors in Go.
  it("matches the Go frozen vector for a delegated transfer", () => {
    const pkg = basePackage();
    pkg.delegationIntent = {
      grantId: "11111111-1111-1111-1111-111111111111",
      spenderIdentityPublicKey: vectorPubkey(0xaa),
      totalAmountSats: 1234,
      receiverAmountsSats: { deadbeef: 1000, cafe: 234 },
    };

    expect(bytesToHex(getTransferPackageSigningPayload(TRANSFER_ID, pkg))).toBe(
      "0e4e193bde433332d25d3a7aa9b892cae4f9e434c4e653f9252307569b8ed806",
    );
  });

  // Mirrors TestTransferPackageWithoutIntentHashesAsPlainV2: the intent fields
  // are appended only when an intent is present, so a transfer without one must
  // hash exactly as it did before delegation existed.
  it("leaves a transfer with no intent hashing as it did before", () => {
    const withUndefined = basePackage();
    const withoutField = basePackage();
    delete (withoutField as { delegationIntent?: unknown }).delegationIntent;

    expect(
      bytesToHex(getTransferPackageSigningPayload(TRANSFER_ID, withUndefined)),
    ).toBe(
      bytesToHex(getTransferPackageSigningPayload(TRANSFER_ID, withoutField)),
    );
  });

  // Mirrors TestTransferPackageEmptyIntentDiffersFromAbsentIntent. Presence
  // decides the meaning of the signature, so these must not collide.
  it("distinguishes an empty intent from an absent one", () => {
    const absent = basePackage();
    const empty = basePackage();
    empty.delegationIntent = {
      grantId: "",
      spenderIdentityPublicKey: new Uint8Array(),
      totalAmountSats: 0,
      receiverAmountsSats: {},
    };

    expect(
      bytesToHex(getTransferPackageSigningPayload(TRANSFER_ID, absent)),
    ).not.toBe(
      bytesToHex(getTransferPackageSigningPayload(TRANSFER_ID, empty)),
    );
  });
});
