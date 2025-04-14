import { Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { getP2TRScriptFromPublicKey } from "./bitcoin.js";
import { Network } from "./network.js";

const TIME_LOCK_INTERVAL = 100;

export function createRefundTx(
  sequence: number,
  nodeOutPoint: TransactionInput,
  amountSats: bigint,
  receivingPubkey: Uint8Array,
  network: Network,
): Transaction {
  const newRefundTx = new Transaction({ allowUnknownOutputs: true });
  newRefundTx.addInput({
    ...nodeOutPoint,
    sequence,
  });

  const refundPkScript = getP2TRScriptFromPublicKey(receivingPubkey, network);

  newRefundTx.addOutput({
    script: refundPkScript,
    amount: amountSats,
  });
  newRefundTx.addOutput(getEphemeralAnchorOutput());

  return newRefundTx;
}

export function getNextTransactionSequence(
  currSequence?: number,
  forRefresh?: boolean,
): {
  nextSequence: number;
  needRefresh: boolean;
} {
  const currentTimelock = (currSequence || 0) & 0xffff;
  const nextTimelock = currentTimelock - TIME_LOCK_INTERVAL;
  if (forRefresh && nextTimelock <= 100 && currentTimelock > 0) {
    return {
      nextSequence: (1 << 30) | nextTimelock,
      needRefresh: true,
    };
  }

  if (nextTimelock <= 0) {
    throw new Error("timelock interval is less or equal to 0");
  }

  return {
    nextSequence: (1 << 30) | nextTimelock,
    needRefresh: nextTimelock <= 100,
  };
}

export function getEphemeralAnchorOutput(): TransactionOutput {
  return {
    script: new Uint8Array([0x51]), // OP_TRUE
    amount: 0n,
  };
}
