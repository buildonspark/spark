import type { Bech32mTokenIdentifier } from "@buildonspark/spark-sdk";
import type { TokenOutputRef } from "@buildonspark/spark-sdk/proto/spark_token";
import type { IssuerSparkWallet } from "../../issuer-wallet/issuer-spark-wallet.js";

export const getSingleIssuerTokenIdentifier = async (
  issuerWallet: IssuerSparkWallet,
): Promise<Bech32mTokenIdentifier> => {
  const tokenIdentifiers = await issuerWallet.getIssuerTokenIdentifiers();
  if (tokenIdentifiers.length !== 1) {
    throw new Error(
      `Expected exactly one issuer token identifier, found ${tokenIdentifiers.length}`,
    );
  }
  return tokenIdentifiers[0];
};

export const getSingleIssuerTokenMetadata = async (
  issuerWallet: IssuerSparkWallet,
) => {
  const tokensMetadata = await issuerWallet.getIssuerTokensMetadata();
  if (tokensMetadata.length !== 1) {
    throw new Error(
      `Expected exactly one issuer token metadata entry, found ${tokensMetadata.length}`,
    );
  }
  return tokensMetadata[0];
};

export const mintSingleIssuerToken = async (
  issuerWallet: IssuerSparkWallet,
  tokenAmount: bigint,
): Promise<string> => {
  const tokenIdentifier = await getSingleIssuerTokenIdentifier(issuerWallet);
  return await issuerWallet.mintTokens({ tokenAmount, tokenIdentifier });
};

export const getSingleIssuerTokenBalance = async (
  issuerWallet: IssuerSparkWallet,
): Promise<{
  tokenIdentifier: Bech32mTokenIdentifier;
  balance: bigint;
}> => {
  const tokenIdentifier = await getSingleIssuerTokenIdentifier(issuerWallet);
  const tokenBalances = await issuerWallet.getIssuerTokenBalances();
  const tokenBalance = tokenBalances.find(
    (balance) => balance.tokenIdentifier === tokenIdentifier,
  );

  return {
    tokenIdentifier,
    balance: tokenBalance?.balance ?? 0n,
  };
};

export const burnSingleIssuerToken = async (
  issuerWallet: IssuerSparkWallet,
  tokenAmount: bigint,
): Promise<string> => {
  const tokenIdentifier = await getSingleIssuerTokenIdentifier(issuerWallet);
  return await issuerWallet.burnTokens({ tokenAmount, tokenIdentifier });
};

export const freezeSingleIssuerToken = async (
  issuerWallet: IssuerSparkWallet,
  sparkAddress: string,
): Promise<{
  impactedTokenOutputs: TokenOutputRef[];
  impactedTokenAmount: bigint;
}> => {
  const tokenIdentifier = await getSingleIssuerTokenIdentifier(issuerWallet);
  return await issuerWallet.freezeTokens({ tokenIdentifier, sparkAddress });
};

export const unfreezeSingleIssuerToken = async (
  issuerWallet: IssuerSparkWallet,
  sparkAddress: string,
): Promise<{
  impactedTokenOutputs: TokenOutputRef[];
  impactedTokenAmount: bigint;
}> => {
  const tokenIdentifier = await getSingleIssuerTokenIdentifier(issuerWallet);
  return await issuerWallet.unfreezeTokens({ tokenIdentifier, sparkAddress });
};
