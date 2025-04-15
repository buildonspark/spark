import { LRCWallet, Lrc20TransactionDto } from "@buildonspark/lrc20-sdk";

import { bytesToHex, bytesToNumberBE } from "@noble/curves/abstract/utils";
import { OutputWithPreviousTransactionData } from "../proto/spark.js";

//TODO: dynamically set these for each leaf based on its metadata in the transaction that created it
const WITHDRAW_BOND_SATS = 10000;
const WITHDRAW_RELATIVE_BLOCK_LOCKTIME = 100;

export async function broadcastL1Withdrawal(
  lrcWallet: LRCWallet,
  outputsToExit: OutputWithPreviousTransactionData[],
  receiverPublicKey: string,
  feeRateSatsPerVb: number = 2.0,
): Promise<{ txid: string }> {
  await lrcWallet.syncWallet();

  let payments = outputsToExit.map(
    ({ output, previousTransactionHash, previousTransactionVout }) => {
      return {
        amount: bytesToNumberBE(output!.tokenAmount),
        tokenPubkey: bytesToHex(output!.tokenPublicKey),
        sats: WITHDRAW_BOND_SATS,
        cltvOutputLocktime: WITHDRAW_RELATIVE_BLOCK_LOCKTIME,
        revocationKey: bytesToHex(output!.revocationCommitment!),
        expiryKey: receiverPublicKey,
        metadata: {
          token_tx_hash: bytesToHex(previousTransactionHash),
          exit_leaf_index: previousTransactionVout,
        },
      };
    },
  );

  const tx = await lrcWallet.prepareSparkExit(payments, feeRateSatsPerVb);

  let txDto = Lrc20TransactionDto.fromLrc20Transaction(tx);

  let txid = await lrcWallet.broadcast(txDto); //.broadcastRawBtcTransaction(tx.bitcoin_tx.toHex());

  return { txid };
}
