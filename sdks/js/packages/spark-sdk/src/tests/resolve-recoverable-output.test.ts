import { describe, expect, it } from "@jest/globals";
import { Transaction } from "@scure/btc-signer";
import { hexToBytes } from "@noble/curves/utils";
import {
  Network as NetworkProto,
  type TreeNode,
  TreeNodeStatus,
} from "../proto/spark.js";
import { resolveRecoverableOutput } from "../spark-wallet/spark-wallet.js";
import { getP2TRScriptFromPublicKey, getTxId } from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";

const LEAF_KEY = hexToBytes(
  "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
);
const OTHER_KEY = hexToBytes(
  "02c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",
);

const leafScript = getP2TRScriptFromPublicKey(LEAF_KEY, Network.REGTEST);
const otherScript = getP2TRScriptFromPublicKey(OTHER_KEY, Network.REGTEST);

/**
 * A node transaction paying `script`. `salt` only varies the input so each
 * fixture gets its own txid, mimicking a renewal chain's near-identical
 * transactions.
 */
function directTx(
  script: Uint8Array,
  amount: bigint,
  salt: number,
  leadingOutputs = 0,
): Uint8Array {
  const tx = new Transaction({ allowUnknownOutputs: true });
  tx.addInput({
    txid: "ab".repeat(31) + salt.toString(16).padStart(2, "0"),
    index: 0,
  });
  for (let i = 0; i < leadingOutputs; i++) {
    tx.addOutput({ script: otherScript, amount: 1000n });
  }
  tx.addOutput({ script, amount });
  return tx.toBytes();
}

function node(overrides: Partial<TreeNode> & Pick<TreeNode, "id">): TreeNode {
  return {
    treeId: "tree",
    value: 1000,
    nodeTx: new Uint8Array(),
    refundTx: new Uint8Array(),
    vout: 0,
    verifyingPublicKey: LEAF_KEY,
    ownerIdentityPublicKey: OTHER_KEY,
    ownerSigningPublicKey: OTHER_KEY,
    signingKeyshare: undefined,
    status: "ON_CHAIN",
    network: NetworkProto.REGTEST,
    createdTime: undefined,
    updatedTime: undefined,
    directTx: new Uint8Array(),
    directRefundTx: new Uint8Array(),
    directFromCpfpRefundTx: new Uint8Array(),
    treenodeStatus: TreeNodeStatus.TREE_NODE_STATUS_ON_CHAIN,
    ...overrides,
  };
}

/**
 * Links a leaf to the generations above it, nearest first, so the chain the
 * resolver walks up matches the tree shape a fixture describes.
 */
function chainOf(...generations: TreeNode[]): TreeNode[] {
  return generations.map((node, i) => ({
    ...node,
    parentNodeId: generations[i + 1]?.id,
  }));
}

function txidOf(rawTx: Uint8Array): string {
  return getTxId(Transaction.fromRaw(rawTx, { allowUnknownOutputs: true }));
}

describe("resolveRecoverableOutput", () => {
  // The leaf and every generation of its renewal chain pay the same key, so
  // status is the only thing that tells the one real output from the rest.
  const staleTx = directTx(leafScript, 900n, 1);
  const confirmedTx = directTx(leafScript, 800n, 2, 1);
  const leafTx = directTx(leafScript, 700n, 3);

  const leaf = node({
    id: "leaf",
    directTx: leafTx,
    status: "WATCHTOWER_EXITED",
    treenodeStatus: TreeNodeStatus.TREE_NODE_STATUS_WATCHTOWER_EXITED,
  });
  const stale = node({
    id: "stale",
    directTx: staleTx,
    status: "SPLITTED",
    treenodeStatus: TreeNodeStatus.TREE_NODE_STATUS_SPLITTED,
  });
  const confirmed = node({ id: "confirmed", directTx: confirmedTx });
  const chain = chainOf(leaf, stale, confirmed);
  const chainLeaf = chain[0]!;

  // Two outputs under the leaf's key, so an override can name the one the
  // default search would not have picked.
  const twoPayoutsTx = (() => {
    const tx = new Transaction({ allowUnknownOutputs: true });
    tx.addInput({ txid: "ab".repeat(31) + "05", index: 0 });
    tx.addOutput({ script: leafScript, amount: 600n });
    tx.addOutput({ script: leafScript, amount: 500n });
    return tx.toBytes();
  })();
  const twoPayouts = node({ id: "twoPayouts", directTx: twoPayoutsTx });

  it("picks the ON_CHAIN ancestor out of a renewal chain", () => {
    const result = resolveRecoverableOutput(chainLeaf, chain, Network.REGTEST);

    expect(result).toEqual({
      txid: txidOf(confirmedTx),
      outputIndex: 1,
      valueSats: 800,
      prevOut: expect.anything(),
    });
  });

  it("does not pick the leaf's own transaction", () => {
    // The leaf's direct tx conflict-spends the very output being recovered, so
    // a signature over it could never confirm.
    const result = resolveRecoverableOutput(leaf, [leaf], Network.REGTEST);

    expect(result).toBeUndefined();
  });

  it("picks the nearest ON_CHAIN generation when several are confirmed", () => {
    // Successive watchtower broadcasts up a renewal chain leave every
    // generation ON_CHAIN, but each one's output is spent by the generation
    // below it — only the nearest is still there to recover.
    const nearerTx = directTx(leafScript, 850n, 6);
    const nearer = node({ id: "nearer", directTx: nearerTx });
    const [chainLeaf, nearest, farthest] = chainOf(leaf, nearer, confirmed);

    // Passed farthest first, since the response is a map and carries no order
    // of its own.
    const result = resolveRecoverableOutput(
      chainLeaf!,
      [chainLeaf!, farthest!, nearest!],
      Network.REGTEST,
    );

    expect(result?.txid).toBe(txidOf(nearerTx));
    expect(result?.valueSats).toBe(850);
  });

  it("honours an explicitly named transaction regardless of status", () => {
    const result = resolveRecoverableOutput(
      chainLeaf,
      chain,
      Network.REGTEST,
      txidOf(staleTx),
    );

    expect(result?.txid).toBe(txidOf(staleTx));
    expect(result?.valueSats).toBe(900);
  });

  it("returns undefined for a txid that is not in the chain", () => {
    const result = resolveRecoverableOutput(
      chainLeaf,
      chain,
      Network.REGTEST,
      "ff".repeat(32),
    );

    expect(result).toBeUndefined();
  });

  it("skips an ancestor whose output pays a different key", () => {
    const sibling = node({
      id: "sibling",
      directTx: directTx(otherScript, 5000n, 4),
    });

    const generations = chainOf(leaf, sibling);

    const result = resolveRecoverableOutput(
      generations[0]!,
      generations,
      Network.REGTEST,
    );

    expect(result).toBeUndefined();
  });

  it("honours an output index override", () => {
    const generations = chainOf(leaf, twoPayouts);

    const result = resolveRecoverableOutput(
      generations[0]!,
      generations,
      Network.REGTEST,
      undefined,
      1,
    );

    // Index 0 is what the unaided search returns, so naming 1 proves the
    // override is what selected this output.
    expect(result?.outputIndex).toBe(1);
    expect(result?.valueSats).toBe(500);
  });

  it("ignores an output index override naming another key's output", () => {
    // The operator makes the same comparison, so honouring this would only
    // produce a transaction it refuses to sign.
    const generations = chainOf(leaf, confirmed);

    const result = resolveRecoverableOutput(
      generations[0]!,
      generations,
      Network.REGTEST,
      undefined,
      0,
    );

    expect(result).toBeUndefined();
  });

  it("ignores an out-of-range output index override", () => {
    const generations = chainOf(leaf, confirmed);

    const result = resolveRecoverableOutput(
      generations[0]!,
      generations,
      Network.REGTEST,
      undefined,
      9,
    );

    expect(result).toBeUndefined();
  });

  it.each([-1, 1.5, NaN])(
    "ignores the malformed output index override %p",
    (outputIndex) => {
      const generations = chainOf(leaf, twoPayouts);

      const result = resolveRecoverableOutput(
        generations[0]!,
        generations,
        Network.REGTEST,
        undefined,
        outputIndex,
      );

      expect(result).toBeUndefined();
    },
  );
});
