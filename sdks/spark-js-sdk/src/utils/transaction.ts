import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TreeNode } from "../proto/spark";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
} from "./bitcoin";
import { getNetwork, Network } from "./network";

const TIME_LOCK_INTERVAL = 100;

export function createRefundTx(
  leaf: TreeNode,
  receivingPubkey: Uint8Array,
  network: Network
): { refundTx: Transaction; sighash: Uint8Array } {
  const tx = getTxFromRawTxBytes(leaf.nodeTx);
  const refundTx = getTxFromRawTxBytes(leaf.refundTx);

  const newRefundTx = new Transaction();
  const sequence = getNextTransactionSequence(refundTx.getInput(0).sequence);
  newRefundTx.addInput({
    txid: tx.id,
    index: 0,
    sequence,
  });

  const finalScriptSig = refundTx.getInput(0).finalScriptSig;
  if (!finalScriptSig) {
    throw new Error(`Final script sig not found for refund tx`);
  }

  const refundP2trAddress = getP2TRAddressFromPublicKey(
    receivingPubkey,
    network
  );
  const refundAddress = Address(getNetwork(network)).decode(refundP2trAddress);
  const refundPkScript = OutScript.encode(refundAddress);

  const amount = refundTx.getOutput(0).amount;
  if (!amount) {
    throw new Error(`Amount not found for refund tx`);
  }
  newRefundTx.addOutput({
    script: refundPkScript,
    amount,
  });

  newRefundTx.updateInput(0, {
    finalScriptSig,
  });

  const sighash = getSigHashFromTx(newRefundTx, 0, tx.getOutput(0));

  return { refundTx: newRefundTx, sighash };
}

export function getNextTransactionSequence(currentSequence?: number) {
  return (1 << 30) | ((currentSequence || 0) & (0xffff - TIME_LOCK_INTERVAL));
}
