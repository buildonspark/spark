import { bytesToNumberBE, equalBytes } from "@noble/curves/utils";
import { OutputWithPreviousTransactionData } from "../proto/spark.js";
import { TokenBalanceMap, TokenOutputsMap } from "../spark-wallet/types.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
} from "./token-identifier.js";

export function sumAvailableTokens(
  outputs: OutputWithPreviousTransactionData[],
): bigint {
  try {
    return outputs.reduce(
      (sum, output) =>
        sum + BigInt(bytesToNumberBE(output.output!.tokenAmount!)),
      BigInt(0),
    );
  } catch (error) {
    return 0n;
  }
}

export function checkIfSelectedOutputsAreAvailable(
  selectedOutputs: OutputWithPreviousTransactionData[],
  tokenOutputs: TokenOutputsMap,
  tokenIdentifier: Bech32mTokenIdentifier,
) {
  const tokenOutputsAvailable = tokenOutputs.get(tokenIdentifier);
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

export function filterTokenBalanceForTokenIdentifier(
  tokenBalances: TokenBalanceMap,
  tokenIdentifier: Bech32mTokenIdentifier,
): { balance: bigint } {
  if (!tokenBalances) {
    return { balance: 0n };
  }

  const tokenIdentifierBytes =
    decodeBech32mTokenIdentifier(tokenIdentifier).tokenIdentifier;

  const tokenBalance = [...tokenBalances.entries()].find(([, info]) =>
    equalBytes(info.tokenMetadata.rawTokenIdentifier, tokenIdentifierBytes),
  );

  if (!tokenBalance) {
    return {
      balance: 0n,
    };
  }
  return {
    balance: tokenBalance[1].balance,
  };
}
