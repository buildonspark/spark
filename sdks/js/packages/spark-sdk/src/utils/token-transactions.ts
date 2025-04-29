import { bytesToHex, bytesToNumberBE } from "@noble/curves/abstract/utils";
import { OutputWithPreviousTransactionData } from "../proto/spark.js";

export function calculateAvailableTokenAmount(
  outputLeaves: OutputWithPreviousTransactionData[],
): bigint {
  return outputLeaves.reduce(
    (sum, output) => sum + BigInt(bytesToNumberBE(output.output!.tokenAmount!)),
    BigInt(0),
  );
}

export function checkIfSelectedOutputsAreAvailable(
  selectedOutputs: OutputWithPreviousTransactionData[],
  tokenOutputs: Map<string, OutputWithPreviousTransactionData[]>,
  tokenPublicKey: Uint8Array,
) {
  const tokenPubKeyHex = bytesToHex(tokenPublicKey);
  const tokenOutputsAvailable = tokenOutputs.get(tokenPubKeyHex);
  if (!tokenOutputsAvailable) {
    return false;
  }
  if (
    selectedOutputs.length === 0 ||
    tokenOutputsAvailable.length < selectedOutputs.length
  ) {
    return false;
  }

  // Create a Set of available token output IDs for O(n + m) lookup
  const availableOutputIds = new Set(
    tokenOutputsAvailable.map((output) => output.output!.id),
  );

  for (const selectedOutput of selectedOutputs) {
    if (
      !selectedOutput.output?.id ||
      !availableOutputIds.has(selectedOutput.output.id)
    ) {
      return false;
    }
  }

  return true;
}

export function filterTokenBalanceForTokenPublicKey(
  tokenBalances: Map<
    string,
    {
      balance: bigint;
    }
  >,
  publicKey: string,
): { balance: bigint } {
  if (!tokenBalances || !tokenBalances.has(publicKey)) {
    return {
      balance: 0n,
    };
  }
  return {
    balance: tokenBalances.get(publicKey)!.balance,
  };
}
