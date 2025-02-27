import { Transaction } from "@scure/btc-signer";
import { TreeNode } from "../proto/spark.js";
import {
  getP2TRScriptFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "./bitcoin.js";
import { Network } from "./network.js";

const TIME_LOCK_INTERVAL = 10;

export function createRefundTx(
  leaf: TreeNode,
  receivingPubkey: Uint8Array,
  network: Network,
): { refundTx: Transaction; sighash: Uint8Array } {
  const tx = getTxFromRawTxBytes(leaf.nodeTx);
  const refundTx = getTxFromRawTxBytes(leaf.refundTx);

  const newRefundTx = new Transaction();
  const sequence = getNextTransactionSequence(refundTx.getInput(0).sequence);
  newRefundTx.addInput({
    txid: getTxId(tx),
    index: 0,
    sequence,
  });

  const refundPkScript = getP2TRScriptFromPublicKey(receivingPubkey, network);

  const amount = refundTx.getOutput(0).amount;
  if (!amount) {
    throw new Error(`Amount not found for refund tx`);
  }
  newRefundTx.addOutput({
    script: refundPkScript,
    amount,
  });

  const sighash = getSigHashFromTx(newRefundTx, 0, tx.getOutput(0));

  return { refundTx: newRefundTx, sighash };
}

export function getNextTransactionSequence(currSequence?: number): number {
  const currentTimelock = (currSequence || 0) & 0xffff;
  if (currentTimelock - TIME_LOCK_INTERVAL <= 0) {
    throw new Error("timelock interval is less or equal to 0");
  }
  return (1 << 30) | (currentTimelock - TIME_LOCK_INTERVAL);
}
