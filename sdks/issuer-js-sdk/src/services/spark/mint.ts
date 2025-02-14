import { SparkWallet } from "spark-js-sdk/src/spark-sdk";
import type { TokenLeafCreationData } from "spark-js-sdk/src/services/tokens";
import { hexToBytes, numberToBytesBE } from "@noble/curves/abstract/utils";
import { constructMintTransaction } from "../../utils/transaction";
import type { TokenTransaction } from "spark-js-sdk/src/proto/spark";

const WITHDRAWAL_BOND_SATS = 10000;
const WITHDRAWAL_BOND_LOCKTIME = Math.floor(Date.now() / 1000) + 24 * 60 * 60; // 24 hours from now in Unix timestamp

export async function mintTokensOnSpark(
  sparkWallet: SparkWallet,
  tokenPublicKey: string,
  amountToIssue: bigint,
) {
  const tokenPublicKeyBytes = hexToBytes(tokenPublicKey);

  const tokenLeafCreationData: TokenLeafCreationData = {
    tokenPublicKey: tokenPublicKeyBytes,
    tokenAmount: numberToBytesBE(amountToIssue, 16)
  };

  const transaction: TokenTransaction = constructMintTransaction(
    [tokenLeafCreationData],
  );

  await sparkWallet.broadcastTokenTransaction(transaction);
}
