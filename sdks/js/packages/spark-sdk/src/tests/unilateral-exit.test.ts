import { describe, expect, it, jest } from "@jest/globals";
import type { Logger } from "@lightsparkdev/core";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { ripemd160 } from "@noble/hashes/legacy";
import { sha256 } from "@noble/hashes/sha2";
import * as btc from "@scure/btc-signer";

import { TreeNode } from "../proto/spark.js";
import { Network } from "../utils/network.js";
import { constructUnilateralExitFeeBumpPackages } from "../utils/unilateral-exit.js";

function hash160(data: Uint8Array): Uint8Array {
  return ripemd160(sha256(data));
}

// Minimal parseable TRUC v3 parent: one input, a placeholder Spark output,
// and a 0-sat OP_TRUE ephemeral anchor as the last output.
// Built via btc.RawTx so the parsed Transaction is treated as finalized
// (every input has a finalScriptSig); constructFeeBumpTx requires that
// because it reads parentTx.id.
function makeTrucParentBytes(
  prevTxidHex: string,
  prevVout: number,
): Uint8Array {
  return btc.RawTx.encode({
    version: 3,
    segwitFlag: false,
    inputs: [
      {
        txid: hexToBytes(prevTxidHex),
        index: prevVout,
        // non-empty so scure treats the parsed input as finalized; the bytes
        // are never executed because we don't broadcast.
        finalScriptSig: new Uint8Array([0x00]),
        sequence: 0xffffffff,
      },
    ],
    outputs: [
      {
        amount: 1_000n,
        script: new Uint8Array([0x00, 0x14, ...new Uint8Array(20)]),
      },
      {
        amount: 0n,
        script: new Uint8Array([0x51]),
      },
    ],
    witnesses: undefined,
    lockTime: 0,
  });
}

describe("unilateral exit", () => {
  it("uses the provided logger for non-fatal transaction parse warnings", async () => {
    const warn = jest.fn();
    const logger = { warn } as unknown as Logger;
    const node = TreeNode.fromPartial({
      id: "node-id",
      nodeTx: new Uint8Array([1, 2, 3]),
      refundTx: new Uint8Array([4, 5, 6]),
      status: "AVAILABLE",
    });

    await expect(
      constructUnilateralExitFeeBumpPackages(
        [bytesToHex(TreeNode.encode(node).finish())],
        [],
        { satPerVbyte: 5 },
        Network.LOCAL,
        undefined,
        logger,
      ),
    ).rejects.toThrow("No UTXOs available for fee bump");

    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining(
        "constructUnilateralExitFeeBumpPackages: unable to parse nodeTx",
      ),
    );
  });

  it("returns fee bumps that do not double-spend each other when batching multiple leaves", async () => {
    // Generate a P2WPKH funding wallet for the test.
    const privateKey = secp256k1.utils.randomSecretKey();
    const publicKey = secp256k1.getPublicKey(privateKey);
    const p2wpkhScript = new Uint8Array([0x00, 0x14, ...hash160(publicKey)]);

    // Single 100k-sat funding UTXO — comfortably covers four small CPFP fee bumps.
    const fundingUtxo = {
      txid: "11".repeat(32),
      vout: 0,
      value: 100_000n,
      script: bytesToHex(p2wpkhScript),
      publicKey: bytesToHex(publicKey),
    };

    // Two leaves with no parents (chain = [leaf]) so each leaf produces
    // exactly two fee bumps: one for node_tx and one for refund_tx.
    const makeLeaf = (id: string, parentSeed: string): TreeNode =>
      TreeNode.fromPartial({
        id,
        nodeTx: makeTrucParentBytes(parentSeed, 0),
        refundTx: makeTrucParentBytes(parentSeed, 1),
        status: "AVAILABLE",
      });

    const leafA = makeLeaf("leaf-a", "aa".repeat(32));
    const leafB = makeLeaf("leaf-b", "bb".repeat(32));

    // Guards against this test silently passing if any earlier expect()
    // is skipped (e.g. result accidentally empty).
    expect.assertions(4);

    const result = await constructUnilateralExitFeeBumpPackages(
      [
        bytesToHex(TreeNode.encode(leafA).finish()),
        bytesToHex(TreeNode.encode(leafB).finish()),
      ],
      [fundingUtxo],
      { satPerVbyte: 5 },
      Network.LOCAL,
    );

    expect(result).toHaveLength(2);
    expect(result[0]?.txPackages).toHaveLength(2);
    expect(result[1]?.txPackages).toHaveLength(2);

    // Behaviorally: the caller of this function will hand these packages to
    // bitcoind via submitpackage. If any two packages reference the same
    // (prev_txid, prev_vout) input, one will be rejected as a mempool conflict
    // and that branch of the exit will stall.
    //
    // Each ephemeral anchor lives on a distinct parent tx, so anchor inputs
    // never collide. The remaining inputs are the funding-side UTXOs threaded
    // through availableUtxos. Pre-fix, leafB's first fee bump would reuse the
    // change UTXO that leafA's refund fee bump had already consumed.
    const seen = new Map<string, string>();
    const collisions: string[] = [];
    for (const leafResult of result) {
      for (let i = 0; i < leafResult.txPackages.length; i++) {
        const pkg = leafResult.txPackages[i]!;
        const feeBumpTx = btc.Transaction.fromPSBT(
          hexToBytes(pkg.feeBumpPsbt!),
        );
        for (let j = 0; j < feeBumpTx.inputsLength; j++) {
          const input = feeBumpTx.getInput(j);
          if (!input.txid) continue;
          const key = `${bytesToHex(input.txid)}:${input.index}`;
          const tag = `${leafResult.leafId}#pkg${i}#in${j}`;
          const prior = seen.get(key);
          if (prior !== undefined) {
            collisions.push(`${key}: ${prior} & ${tag}`);
          } else {
            seen.set(key, tag);
          }
        }
      }
    }
    expect(collisions).toEqual([]);
  });
});
