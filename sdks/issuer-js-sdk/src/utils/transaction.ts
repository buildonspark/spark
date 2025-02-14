import { TokenTransaction } from 'spark-js-sdk/src/proto/spark';
import { randomUUID } from "crypto";
import type { TokenLeafCreationData } from 'spark-js-sdk/src/services/tokens';

const WITHDRAWAL_BOND_SATS = 10000;
const WITHDRAWAL_BOND_LOCKTIME = Math.floor(Date.now() / 1000) + 24 * 60 * 60; // 24 hours from now in Unix timestamp

export function constructMintTransaction(leafDataArray: TokenLeafCreationData[]): TokenTransaction {
  if (!leafDataArray || leafDataArray.length === 0) {
    throw new Error("At least one TokenLeafCreationData must be provided");
  }

  const tokenTransaction: TokenTransaction = {
    tokenInput: {
      $case: "mintInput",
      mintInput: {
        issuerPublicKey: leafDataArray[0].tokenPublicKey,
      },
    },
    outputLeaves: leafDataArray.map(leafData => ({
      id: randomUUID(),
      ownerPublicKey: leafData.tokenPublicKey,
      withdrawalBondSats: WITHDRAWAL_BOND_SATS,
      withdrawalLocktime: WITHDRAWAL_BOND_LOCKTIME,
      tokenPublicKey: leafData.tokenPublicKey,
      tokenAmount: leafData.tokenAmount,
      revocationPublicKey: new Uint8Array(0), // Will be filled in by the SOs after start_token_transaction
    })),
    // These get filled in later
    sparkOperatorIdentityPublicKeys: [],
  };

  return tokenTransaction;
}