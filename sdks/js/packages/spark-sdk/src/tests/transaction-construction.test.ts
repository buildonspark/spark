import { describe, expect, it } from "@jest/globals";
import { hexToBytes } from "@noble/curves/utils";
import { getTxFromRawTxHex } from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import {
  createRootNodeTx,
  createInitialTimelockRefundTxs,
  createZeroTimelockNodeTx,
  createInitialTimelockNodeTx,
  createDecrementedTimelockRefundTxs,
  getCurrentTimelock,
  INITIAL_SEQUENCE,
  TIME_LOCK_INTERVAL,
  DIRECT_TIMELOCK_OFFSET,
  DEFAULT_FEE_SATS,
} from "../utils/transaction.js";
import { getSparkFrost } from "../spark-bindings/spark-bindings.js";

describe("transaction construction via Rust bindings", () => {
  // A known-good tx with a P2TR output of 100,000 sats
  const parentTxHex =
    "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000";

  const receivingPubkey = hexToBytes(
    "03ef261da8259f5ea86abe1b7d658ebd13fe2b2ce2418dabe854ccec67bdb9ba87",
  );

  describe("createRootNodeTx", () => {
    it("produces valid CPFP and direct node transactions", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx, directNodeTx } = await createRootNodeTx(
        parentTx,
        0,
        Network.MAINNET,
      );

      // Both transactions should be valid
      expect(nodeTx).toBeDefined();
      expect(directNodeTx).toBeDefined();

      // Root node uses sequence 0 for CPFP
      expect(nodeTx.getInput(0).sequence).toBe(0);
      // Direct node uses DIRECT_TIMELOCK_OFFSET
      expect(directNodeTx.getInput(0).sequence).toBe(DIRECT_TIMELOCK_OFFSET);

      // CPFP node tx keeps full parent amount (fee paid via CPFP anchor)
      const parentAmount = parentTx.getOutput(0).amount!;
      expect(nodeTx.getOutput(0).amount).toBe(parentAmount);
      // Direct node tx subtracts fee
      const expectedDirectAmount = parentAmount - BigInt(DEFAULT_FEE_SATS);
      expect(directNodeTx.getOutput(0).amount).toBe(expectedDirectAmount);

      // CPFP node tx has ephemeral anchor output, direct does not
      expect(nodeTx.outputsLength).toBe(2);
      expect(directNodeTx.outputsLength).toBe(1);
    });
  });

  describe("createZeroTimelockNodeTx", () => {
    it("creates node txs with zero timelock and direct offset", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx, directNodeTx } = await createZeroTimelockNodeTx(
        parentTx,
        Network.MAINNET,
      );

      expect(getCurrentTimelock(nodeTx.getInput(0).sequence)).toBe(0);
      expect(getCurrentTimelock(directNodeTx.getInput(0).sequence)).toBe(
        DIRECT_TIMELOCK_OFFSET,
      );
    });
  });

  describe("createInitialTimelockNodeTx", () => {
    it("creates node txs with initial timelock", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx, directNodeTx } = await createInitialTimelockNodeTx(
        parentTx,
        Network.MAINNET,
      );

      expect(getCurrentTimelock(nodeTx.getInput(0).sequence)).toBe(
        INITIAL_SEQUENCE,
      );
      expect(getCurrentTimelock(directNodeTx.getInput(0).sequence)).toBe(
        INITIAL_SEQUENCE + DIRECT_TIMELOCK_OFFSET,
      );
    });
  });

  describe("createInitialTimelockRefundTxs", () => {
    it("produces refund txs from node tx without direct node tx", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx } = await createRootNodeTx(parentTx, 0, Network.MAINNET);

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        await createInitialTimelockRefundTxs({
          nodeTx,
          receivingPubkey,
          network: Network.MAINNET,
        });

      expect(cpfpRefundTx).toBeDefined();
      expect(directFromCpfpRefundTx).toBeDefined();
      // No direct refund when there's no direct node tx and it's a zero-timelock node
      expect(directRefundTx).toBeUndefined();

      // Refund should spend the node tx output
      expect(cpfpRefundTx.getInput(0).sequence).toBe(INITIAL_SEQUENCE);
    });

    it("produces all three refund txs with direct node tx", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx, directNodeTx } = await createInitialTimelockNodeTx(
        parentTx,
        Network.MAINNET,
      );

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        await createInitialTimelockRefundTxs({
          nodeTx,
          directNodeTx,
          receivingPubkey,
          network: Network.MAINNET,
        });

      expect(cpfpRefundTx).toBeDefined();
      expect(directRefundTx).toBeDefined();
      expect(directFromCpfpRefundTx).toBeDefined();
    });
  });

  describe("createDecrementedTimelockRefundTxs", () => {
    it("decrements the timelock by TIME_LOCK_INTERVAL", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx, directNodeTx } = await createInitialTimelockNodeTx(
        parentTx,
        Network.MAINNET,
      );

      const { cpfpRefundTx } = await createDecrementedTimelockRefundTxs({
        nodeTx,
        directNodeTx,
        sequence: INITIAL_SEQUENCE,
        receivingPubkey,
        network: Network.MAINNET,
      });

      const expectedTimelock = INITIAL_SEQUENCE - TIME_LOCK_INTERVAL;
      expect(getCurrentTimelock(cpfpRefundTx.getInput(0).sequence)).toBe(
        expectedTimelock,
      );
    });
  });

  describe("computeMultiInputSighash", () => {
    it("computes sighash for a single input", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx } = await createRootNodeTx(parentTx, 0, Network.MAINNET);

      const sparkFrost = getSparkFrost();
      const parentOutput = parentTx.getOutput(0);
      const sighash = await sparkFrost.computeMultiInputSighash(
        nodeTx.toBytes(true),
        0,
        [parentOutput.script!],
        [Number(parentOutput.amount!)],
      );

      expect(sighash).toBeDefined();
      expect(sighash.length).toBe(32);
    });

    it("produces consistent sighash for same inputs", async () => {
      const parentTx = getTxFromRawTxHex(parentTxHex);
      const { nodeTx } = await createRootNodeTx(parentTx, 0, Network.MAINNET);

      const sparkFrost = getSparkFrost();
      const parentOutput = parentTx.getOutput(0);
      const sighash1 = await sparkFrost.computeMultiInputSighash(
        nodeTx.toBytes(true),
        0,
        [parentOutput.script!],
        [Number(parentOutput.amount!)],
      );
      const sighash2 = await sparkFrost.computeMultiInputSighash(
        nodeTx.toBytes(true),
        0,
        [parentOutput.script!],
        [Number(parentOutput.amount!)],
      );

      expect(sighash1).toEqual(sighash2);
    });
  });
});
