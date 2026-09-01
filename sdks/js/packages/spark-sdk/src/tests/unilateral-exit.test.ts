import { describe, expect, it, jest } from "@jest/globals";
import type { Logger } from "@lightsparkdev/core";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { ripemd160 } from "@noble/hashes/legacy";
import { sha256 } from "@noble/hashes/sha2";
import * as btc from "@scure/btc-signer";

import { TreeNode } from "../proto/spark.js";
import { getTxFromRawTxHex, getTxId } from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import { constructUnilateralExitFeeBumpPackages } from "../utils/unilateral-exit.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";

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

describe("unilateral exit stateless resume", () => {
  // A unilateral exit is two-phase: the exit chain is broadcast first, and
  // the leaf's CSV-timelocked refund is broadcast once the timelock matures.
  // A caller that rebuilds packages from chain state on a later run must
  // keep receiving the leaf's refund until that refund (or an equivalent
  // direct variant, which is what a watchtower broadcasts) is itself on
  // chain; otherwise the exit silently completes with the leaf's value
  // stranded in the timelock-encumbered node output.
  const fundingPrivateKey = hexToBytes("07".repeat(32));
  const fundingPublicKey = secp256k1.getPublicKey(fundingPrivateKey);
  const fundingUtxo = {
    txid: "11".repeat(32),
    vout: 0,
    value: 100_000n,
    script: bytesToHex(
      new Uint8Array([0x00, 0x14, ...hash160(fundingPublicKey)]),
    ),
    publicKey: bytesToHex(fundingPublicKey),
  };

  const makeLeaf = (id: string, parentSeed: string): TreeNode =>
    TreeNode.fromPartial({
      id,
      nodeTx: makeTrucParentBytes(parentSeed, 0),
      refundTx: makeTrucParentBytes(parentSeed, 1),
      status: "AVAILABLE",
    });

  const txidOf = (txBytes: Uint8Array): string =>
    getTxId(getTxFromRawTxHex(bytesToHex(txBytes)));

  let getRawTransactionSpy: jest.SpiedFunction<
    BitcoinFaucet["getRawTransaction"]
  >;

  // isTxBroadcast(Network.LOCAL) resolves through the faucet's
  // getRawTransaction: a resolved call means the tx is on chain, a rejected
  // call means it is not.
  const setOnChainTxids = (onChainTxids: ReadonlySet<string>) => {
    getRawTransactionSpy.mockImplementation((txid: string) => {
      if (onChainTxids.has(txid)) {
        return Promise.resolve(
          {} as Awaited<ReturnType<BitcoinFaucet["getRawTransaction"]>>,
        );
      }
      return Promise.reject(
        new Error("No such mempool or blockchain transaction"),
      );
    });
  };

  const buildPackages = (leaf: TreeNode) =>
    constructUnilateralExitFeeBumpPackages(
      [bytesToHex(TreeNode.encode(leaf).finish())],
      [fundingUtxo],
      { satPerVbyte: 5 },
      Network.LOCAL,
    );

  beforeEach(() => {
    getRawTransactionSpy = jest.spyOn(
      BitcoinFaucet.prototype,
      "getRawTransaction",
    );
  });

  afterEach(() => {
    getRawTransactionSpy.mockRestore();
  });

  it("emits the leaf's node tx and refund when neither is on chain", async () => {
    const leaf = makeLeaf("leaf-a", "aa".repeat(32));
    setOnChainTxids(new Set());

    expect.assertions(4);

    const result = await buildPackages(leaf);

    expect(result).toHaveLength(1);
    const txPackages = result[0]!.txPackages;
    expect(txPackages.map((pkg) => pkg.tx)).toEqual([
      bytesToHex(leaf.nodeTx),
      bytesToHex(leaf.refundTx),
    ]);
    for (const pkg of txPackages) {
      expect(pkg.feeBumpPsbt).toBeDefined();
    }
  });

  it("still surfaces the pending refund when the leaf's node tx is already on chain", async () => {
    const leaf = makeLeaf("leaf-a", "aa".repeat(32));
    setOnChainTxids(new Set([txidOf(leaf.nodeTx)]));

    const result = await buildPackages(leaf);

    expect(result).toHaveLength(1);
    const txPackages = result[0]!.txPackages;
    expect(txPackages.map((pkg) => pkg.tx)).toEqual([
      bytesToHex(leaf.refundTx),
    ]);
    // The emitted refund must carry a well-formed fee bump so it can be
    // broadcast once its CSV timelock matures.
    btc.Transaction.fromPSBT(hexToBytes(txPackages[0]!.feeBumpPsbt!));
  });

  it("omits the refund once the refund itself is on chain", async () => {
    const leaf = makeLeaf("leaf-a", "aa".repeat(32));
    setOnChainTxids(new Set([txidOf(leaf.nodeTx), txidOf(leaf.refundTx)]));

    const result = await buildPackages(leaf);

    expect(result).toHaveLength(1);
    expect(result[0]!.txPackages).toHaveLength(0);
  });

  it.each([
    ["directRefundTx", 2],
    ["directFromCpfpRefundTx", 3],
  ])(
    "omits the refund when the leaf's %s is on chain",
    async (directField, vout) => {
      const leaf = TreeNode.fromPartial({
        id: "leaf-a",
        nodeTx: makeTrucParentBytes("aa".repeat(32), 0),
        refundTx: makeTrucParentBytes("aa".repeat(32), 1),
        [directField]: makeTrucParentBytes("aa".repeat(32), vout),
        status: "AVAILABLE",
      });
      const onChainTxids = new Set([
        txidOf(leaf.nodeTx),
        txidOf(
          directField === "directRefundTx"
            ? leaf.directRefundTx
            : leaf.directFromCpfpRefundTx,
        ),
      ]);
      setOnChainTxids(onChainTxids);

      const result = await buildPackages(leaf);

      expect(result).toHaveLength(1);
      expect(result[0]!.txPackages).toHaveLength(0);
    },
  );
});
