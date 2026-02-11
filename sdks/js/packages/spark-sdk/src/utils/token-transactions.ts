import { bytesToNumberBE, equalBytes } from "@noble/curves/utils";
import { OutputWithPreviousTransactionData } from "../proto/spark_token.js";
import { TokenBalanceMap } from "../spark-wallet/types.js";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
} from "./token-identifier.js";

export function sumTokenOutputs(
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

export function filterTokenBalanceForTokenIdentifier(
  tokenBalances: TokenBalanceMap,
  tokenIdentifier: Bech32mTokenIdentifier,
): { ownedBalance: bigint; availableToSendBalance: bigint } {
  if (!tokenBalances) {
    return { ownedBalance: 0n, availableToSendBalance: 0n };
  }

  const tokenIdentifierBytes =
    decodeBech32mTokenIdentifier(tokenIdentifier).tokenIdentifier;

  const tokenBalance = [...tokenBalances.entries()].find(([, info]) =>
    equalBytes(info.tokenMetadata.rawTokenIdentifier, tokenIdentifierBytes),
  );

  if (!tokenBalance) {
    return {
      ownedBalance: 0n,
      availableToSendBalance: 0n,
    };
  }
  return {
    ownedBalance: tokenBalance[1].ownedBalance,
    availableToSendBalance: tokenBalance[1].availableToSendBalance,
  };
}
