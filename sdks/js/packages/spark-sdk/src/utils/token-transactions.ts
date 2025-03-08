import { bytesToHex, bytesToNumberBE } from "@noble/curves/abstract/utils";
import {
  LeafWithPreviousTransactionData,
  TokenTransaction,
} from "../proto/spark.js";
import { hashTokenTransaction } from "./token-hashing.js";

export function getTokenLeavesSum(
  leaves: LeafWithPreviousTransactionData[],
): bigint {
  return leaves.reduce(
    (sum, leaf) => sum + BigInt(bytesToNumberBE(leaf.leaf!.tokenAmount!)),
    BigInt(0),
  );
}

export function extractOutputLeaves(
  fullTokenTransaction: TokenTransaction,
): LeafWithPreviousTransactionData[] {
  const outputLeaves: LeafWithPreviousTransactionData[] = [];
  const hash = hashTokenTransaction(fullTokenTransaction, true);

  fullTokenTransaction.outputLeaves!.forEach((output, index) => {
    outputLeaves.push({
      leaf: output,
      previousTransactionHash: hash!,
      previousTransactionVout: index,
    });
  });
  return outputLeaves;
}

export function calculateAvailableTokenAmount(
  outputLeaves: LeafWithPreviousTransactionData[],
): bigint {
  return outputLeaves.reduce(
    (sum, leaf) => sum + BigInt(bytesToNumberBE(leaf.leaf!.tokenAmount!)),
    BigInt(0),
  );
}

export function checkIfSelectedLeavesAreAvailable(
  selectedLeaves: LeafWithPreviousTransactionData[],
  tokenLeaves: Map<string, LeafWithPreviousTransactionData[]>,
  tokenPublicKey: Uint8Array,
) {
  const tokenPubKeyHex = bytesToHex(tokenPublicKey);
  const tokenLeavesAvailable = tokenLeaves.get(tokenPubKeyHex);
  if (!tokenLeavesAvailable) {
    return false;
  }
  if (
    selectedLeaves.length === 0 ||
    tokenLeavesAvailable.length < selectedLeaves.length
  ) {
    return false;
  }

  // Create a Set of available leaf IDs for O(n + m) lookup
  const availableLeafIds = new Set(
    tokenLeavesAvailable.map((leaf) => leaf.leaf!.id),
  );

  for (const selectedLeaf of selectedLeaves) {
    if (!selectedLeaf.leaf?.id || !availableLeafIds.has(selectedLeaf.leaf.id)) {
      return false;
    }
  }

  return true;
}
