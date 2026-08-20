import { hexToBytes } from "@noble/hashes/utils";
import { bytesToHex } from "@noble/curves/utils";
import {
  PartialTokenTransaction,
  type PartialTokenOutput,
} from "../proto/spark_token.js";
import { SparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.node.js";
import {
  getSparkTokenPrimitives,
  setSparkTokenPrimitivesOnce,
} from "../token-primitives-bindings/token-primitives-bindings.js";
import { hashPartialTokenTransaction } from "../utils/token-hashing.js";

const OWNER_KEY_HEX =
  "02ca75659458529755b77663f18282f4aa130313e098fac40deffb1208207a2ffe";
const TOKEN_ID_HEX =
  "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451";
const OPERATOR_KEY_HEX =
  "033e40d72117ee89f7bda15d2b3d779843e6721e8e4c5078c192b50fb3782de2f5";

function deterministicPull(): PartialTokenTransaction {
  const output: PartialTokenOutput = {
    ownerPublicKey: hexToBytes(OWNER_KEY_HEX),
    tokenIdentifier: hexToBytes(TOKEN_ID_HEX),
    withdrawBondSats: 10000,
    withdrawRelativeBlockLocktime: 1000,
    tokenAmount: hexToBytes("00000000000000000000000000002710"),
  };
  return {
    version: 3,
    tokenTransactionMetadata: {
      network: 2,
      sparkOperatorIdentityPublicKeys: [hexToBytes(OPERATOR_KEY_HEX)],
      validityDurationSeconds: 180,
      clientCreatedTimestamp: new Date(1747337980820),
      invoiceAttachments: [],
    },
    tokenInputs: {
      $case: "transferInput",
      transferInput: {
        outputsToSpend: [
          {
            prevTokenTransactionHash: new Uint8Array(32).fill(4),
            prevTokenTransactionVout: 0,
          },
        ],
      },
    },
    partialTokenOutputs: [output],
  };
}

setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

// Frozen cross-language vector: the same partial pull hashed by Go
// (utils.HashTokenTransaction over the V2 shape, which every SO recomputes).
// A change here means the spender signs something no operator reproduces.
const KNOWN_PARTIAL_PULL_HASH_HEX =
  "ffb036e1ef890ae207219e569ec89de2876ea0357fcfcf3c5957214e8560a55a";

describe("allowance pull partial-transaction hash", () => {
  it("matches the frozen Go vector", async () => {
    const hash = await getSparkTokenPrimitives().hashPartialTokenTransaction(
      PartialTokenTransaction.encode(deterministicPull()).finish(),
    );

    expect(bytesToHex(hash)).toBe(KNOWN_PARTIAL_PULL_HASH_HEX);
  });

  it("agrees between the manual hasher and the token primitives bindings", async () => {
    const partial = deterministicPull();

    const manual = await hashPartialTokenTransaction(partial);
    const viaBindings =
      await getSparkTokenPrimitives().hashPartialTokenTransaction(
        PartialTokenTransaction.encode(partial).finish(),
      );

    expect(bytesToHex(viaBindings)).toBe(bytesToHex(manual));
    expect(bytesToHex(manual)).toBe(KNOWN_PARTIAL_PULL_HASH_HEX);
  });
});
