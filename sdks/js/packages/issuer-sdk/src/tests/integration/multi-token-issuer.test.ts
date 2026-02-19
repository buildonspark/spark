import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { TEST_CONFIGS } from "./test-configs.js";
import { IssuerSparkWallet } from "../../issuer-wallet/issuer-spark-wallet.js";
import {
  burnSingleIssuerToken,
  freezeSingleIssuerToken,
  getSingleIssuerTokenBalance,
  getSingleIssuerTokenIdentifier,
  mintSingleIssuerToken,
  unfreezeSingleIssuerToken,
} from "../utils/multi-token-utils.js";

const TX_HASH_REGEX = /^[a-f0-9]{64}$/i; // valid tx hash: hex string of 64 characters

const TOKEN_AMOUNT = 100n;
const TOKEN_ONE_NAME = "Token1";
const TOKEN_ONE_TICKER = "TK1";
const TOKEN_ONE_METADATA = new Uint8Array([1, 2, 3]);
const TOKEN_TWO_NAME = "Token2";
const TOKEN_TWO_TICKER = "TK2";
const TOKEN_TWO_METADATA = new Uint8Array([4, 5, 6]);

const TOKEN_ONE_CREATE_TRANSACTION_PARAMS = {
  tokenName: TOKEN_ONE_NAME,
  tokenTicker: TOKEN_ONE_TICKER,
  decimals: 0,
  isFreezable: true,
  maxSupply: 1000n,
  extraMetadata: TOKEN_ONE_METADATA,
  returnIdentifierForCreate: true,
} as const;

const TOKEN_TWO_CREATE_TRANSACTION_PARAMS = {
  tokenName: TOKEN_TWO_NAME,
  tokenTicker: TOKEN_TWO_TICKER,
  decimals: 0,
  isFreezable: true,
  maxSupply: 1000n,
  extraMetadata: TOKEN_TWO_METADATA,
  returnIdentifierForCreate: true,
} as const;

const setupMultipleTokens = async (issuerWallet: IssuerSparkWallet) => {
  const firstCreateTransactionDetails = await issuerWallet.createToken(
    TOKEN_ONE_CREATE_TRANSACTION_PARAMS,
  );
  const secondCreateTransactionDetails = await issuerWallet.createToken(
    TOKEN_TWO_CREATE_TRANSACTION_PARAMS,
  );
  return {
    firstTokenIdentifier: firstCreateTransactionDetails.tokenIdentifier,
    secondTokenIdentifier: secondCreateTransactionDetails.tokenIdentifier,
  };
};

describe.each(TEST_CONFIGS)(
  "multi token issuer tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should successfully create multiple tokens with different parameters", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const { firstTokenIdentifier, secondTokenIdentifier } =
        await setupMultipleTokens(issuerWallet);

      expect(firstTokenIdentifier).toBeDefined();
      expect(firstTokenIdentifier.length).toBeGreaterThan(0);
      expect(secondTokenIdentifier).toBeDefined();
      expect(secondTokenIdentifier.length).toBeGreaterThan(0);

      const metadata = await issuerWallet.getIssuerTokensMetadata();
      expect(metadata.length).toEqual(2);
      expect(metadata[0].tokenName).toEqual("Token1");
      expect(metadata[0].tokenTicker).toEqual("TK1");
      expect(metadata[1].tokenName).toEqual("Token2");
      expect(metadata[1].tokenTicker).toEqual("TK2");
    });

    it("get issuer tokens metadata should only return tokens owned by the issuer", async () => {
      const { wallet: aliceWallet } = await IssuerSparkWalletTesting.initialize(
        {
          options: config,
        },
      );
      const { wallet: bobWallet } = await IssuerSparkWalletTesting.initialize({
        options: config,
      });

      const {
        firstTokenIdentifier: bobCoinOne,
        secondTokenIdentifier: bobCoinTwo,
      } = await setupMultipleTokens(bobWallet);
      const {
        firstTokenIdentifier: aliceCoinOne,
        secondTokenIdentifier: aliceCoinTwo,
      } = await setupMultipleTokens(aliceWallet);

      const bobWalletForBobCoins = await bobWallet.getIssuerTokensMetadata([
        bobCoinOne,
        bobCoinTwo,
      ]);
      expect(bobWalletForBobCoins.length).toEqual(2);
      expect(
        bobWalletForBobCoins.map((m) => ({
          name: m.tokenName,
          ticker: m.tokenTicker,
        })),
      ).toEqual(
        expect.arrayContaining([
          { name: "Token1", ticker: "TK1" },
          { name: "Token2", ticker: "TK2" },
        ]),
      );
      const bobWalletForAliceCoins = await bobWallet.getIssuerTokensMetadata([
        aliceCoinOne,
        aliceCoinTwo,
      ]);
      expect(bobWalletForAliceCoins.length).toEqual(0);
    });

    it("should fail to create multiple tokens with the same parameters", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const firstCreateTransactionDetails = await issuerWallet.createToken(
        TOKEN_ONE_CREATE_TRANSACTION_PARAMS,
      );

      expect(firstCreateTransactionDetails).toBeDefined();
      expect(firstCreateTransactionDetails.tokenIdentifier).toBeDefined();
      expect(
        firstCreateTransactionDetails.tokenIdentifier.length,
      ).toBeGreaterThan(0);
      expect(firstCreateTransactionDetails.transactionHash).toBeDefined();
      expect(
        firstCreateTransactionDetails.transactionHash.length,
      ).toBeGreaterThan(0);

      await expect(
        issuerWallet.createToken(TOKEN_ONE_CREATE_TRANSACTION_PARAMS),
      ).rejects.toThrow();
    });

    it("should fail to execute legacy methods that do not support multiple tokens and succeed with new methods that accept token identifiers", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      const issuerSparkAddress = await issuerWallet.getSparkAddress();
      const { wallet: receiverWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const receiverAddress = await receiverWallet.getSparkAddress();

      const { firstTokenIdentifier, secondTokenIdentifier } =
        await setupMultipleTokens(issuerWallet);

      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(issuerWallet.getIssuerTokenIdentifier()).rejects.toThrow();

      // Multi token issuer method - should succeed to get token identifiers
      const tokenIdentifiers = await issuerWallet.getIssuerTokenIdentifiers();
      expect(tokenIdentifiers.length).toEqual(2);
      expect(tokenIdentifiers[0]).toEqual(firstTokenIdentifier);
      expect(tokenIdentifiers[1]).toEqual(secondTokenIdentifier);

      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(issuerWallet.getIssuerTokenMetadata()).rejects.toThrow();

      // Multi token issuer method - should succeed to get token metadata
      const metadataArr = await issuerWallet.getIssuerTokensMetadata();
      expect(metadataArr.length).toEqual(2);
      expect(metadataArr[0].tokenName).toEqual(TOKEN_ONE_NAME);
      expect(metadataArr[0].tokenTicker).toEqual(TOKEN_ONE_TICKER);
      expect(metadataArr[1].tokenName).toEqual(TOKEN_TWO_NAME);
      expect(metadataArr[1].tokenTicker).toEqual(TOKEN_TWO_TICKER);

      // === Minting tokens ===
      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(issuerWallet.mintTokens(1n)).rejects.toThrow();

      // Multi token issuer method - should succeed to mint tokens with a token identifier
      const firstMintHash = await issuerWallet.mintTokens({
        tokenAmount: 100n,
        tokenIdentifier: firstTokenIdentifier,
      });
      expect(firstMintHash).toBeDefined();
      expect(firstMintHash).toMatch(TX_HASH_REGEX);

      // Multi token issuer method - should succeed to mint tokens with a token identifier
      const secondMintHash = await issuerWallet.mintTokens({
        tokenAmount: 100n,
        tokenIdentifier: secondTokenIdentifier,
      });
      expect(secondMintHash).toBeDefined();
      expect(secondMintHash).toMatch(TX_HASH_REGEX);

      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(issuerWallet.getIssuerTokenBalance()).rejects.toThrow();

      // Multi token issuer method - should succeed to get token balances
      const balances = await issuerWallet.getIssuerTokenBalances();
      const firstBalance = balances.find(
        (b) => b.tokenIdentifier === firstTokenIdentifier,
      );
      const secondBalance = balances.find(
        (b) => b.tokenIdentifier === secondTokenIdentifier,
      );

      expect(firstBalance?.balance).toEqual(100n);
      expect(secondBalance?.balance).toEqual(100n);

      // Multi token issuer method - should succeed to transfer tokens with a token identifier
      const firstTransferResponse = await issuerWallet.transferTokens({
        tokenAmount: TOKEN_AMOUNT,
        tokenIdentifier: firstTokenIdentifier,
        receiverSparkAddress: receiverAddress,
      });
      expect(firstTransferResponse).toBeDefined();
      expect(firstTransferResponse).toMatch(TX_HASH_REGEX);

      const secondTransferResponse = await issuerWallet.transferTokens({
        tokenAmount: TOKEN_AMOUNT,
        tokenIdentifier: secondTokenIdentifier,
        receiverSparkAddress: receiverAddress,
      });
      expect(secondTransferResponse).toBeDefined();
      expect(secondTransferResponse).toMatch(TX_HASH_REGEX);

      const receiverBalances = await receiverWallet.getBalance();
      const receiverFirstBalance =
        receiverBalances.tokenBalances.get(firstTokenIdentifier);
      const receiverSecondBalance = receiverBalances.tokenBalances.get(
        secondTokenIdentifier,
      );
      expect(receiverFirstBalance?.ownedBalance).toEqual(TOKEN_AMOUNT);
      expect(receiverSecondBalance?.ownedBalance).toEqual(TOKEN_AMOUNT);

      // === Freezing tokens ===
      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(
        issuerWallet.freezeTokens(receiverAddress),
      ).rejects.toThrow();

      // Multi token issuer method - should succeed when using freezeTokens with a token identifier
      const freezeResponse = await issuerWallet.freezeTokens({
        tokenIdentifier: firstTokenIdentifier,
        sparkAddress: receiverAddress,
      });
      expect(freezeResponse.impactedTokenOutputs.length).toBeGreaterThan(0);
      expect(freezeResponse.impactedTokenAmount).toEqual(TOKEN_AMOUNT);

      // Should fail to transfer tokens because the outputs are frozen
      await expect(
        receiverWallet.transferTokens({
          tokenAmount: TOKEN_AMOUNT,
          tokenIdentifier: firstTokenIdentifier,
          receiverSparkAddress: issuerSparkAddress,
        }),
      ).rejects.toThrow();

      // Multi token issuer method - should succeed to transfer tokens with a token identifier
      const transferBackToIssuerOfNeverfrozenToken =
        await receiverWallet.transferTokens({
          tokenAmount: TOKEN_AMOUNT,
          tokenIdentifier: secondTokenIdentifier,
          receiverSparkAddress: issuerSparkAddress,
        });
      expect(transferBackToIssuerOfNeverfrozenToken).toBeDefined();
      expect(transferBackToIssuerOfNeverfrozenToken).toMatch(TX_HASH_REGEX);

      // === Unfreezing tokens ===
      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(
        issuerWallet.unfreezeTokens(receiverAddress),
      ).rejects.toThrow();

      // Multi token issuer method - should succeed when using unfreezeTokens with a token identifier
      const unfreezeResponse = await issuerWallet.unfreezeTokens({
        tokenIdentifier: firstTokenIdentifier,
        sparkAddress: receiverAddress,
      });
      expect(unfreezeResponse.impactedTokenOutputs.length).toBeGreaterThan(0);
      expect(unfreezeResponse.impactedTokenAmount).toEqual(TOKEN_AMOUNT);

      // Wait for local token output lock from before to expire before spending once-frozen outputs.
      await new Promise((resolve) =>
        setTimeout(resolve, config.tokenOutputLockExpiryMs),
      );

      // Outputs unfrozen, transfer should succeed
      const transferBackToIssuerOfOnceFrozenToken =
        await receiverWallet.transferTokens({
          tokenAmount: TOKEN_AMOUNT,
          tokenIdentifier: firstTokenIdentifier,
          receiverSparkAddress: issuerSparkAddress,
        });
      expect(transferBackToIssuerOfOnceFrozenToken).toBeDefined();
      expect(transferBackToIssuerOfOnceFrozenToken).toMatch(TX_HASH_REGEX);

      const receiverBalancesAfterTransferBack =
        await receiverWallet.getBalance();
      const receiverFirstBalanceAfterTransferBack =
        receiverBalancesAfterTransferBack.tokenBalances.get(
          firstTokenIdentifier,
        );
      const receiverSecondBalanceAfterTransferBack =
        receiverBalancesAfterTransferBack.tokenBalances.get(
          secondTokenIdentifier,
        );
      expect(
        receiverFirstBalanceAfterTransferBack?.ownedBalance,
      ).toBeUndefined();
      expect(
        receiverSecondBalanceAfterTransferBack?.ownedBalance,
      ).toBeUndefined();

      // Verify that the issuer has the correct balances
      const issuerBalances = await issuerWallet.getIssuerTokenBalances();
      const issuerFirstBalance = issuerBalances.find(
        (b) => b.tokenIdentifier === firstTokenIdentifier,
      );
      const issuerSecondBalance = issuerBalances.find(
        (b) => b.tokenIdentifier === secondTokenIdentifier,
      );
      expect(issuerFirstBalance?.balance).toEqual(TOKEN_AMOUNT);
      expect(issuerSecondBalance?.balance).toEqual(TOKEN_AMOUNT);

      // === Burning tokens ===
      // Legacy single token issuer method - should fail when multiple tokens are created
      await expect(issuerWallet.burnTokens(100n)).rejects.toThrow();

      // Multi token issuer method - should succeed to burn tokens with a token identifier
      const burnResponse = await issuerWallet.burnTokens({
        tokenAmount: TOKEN_AMOUNT,
        tokenIdentifier: firstTokenIdentifier,
      });
      expect(burnResponse).toBeDefined();
      expect(burnResponse).toMatch(TX_HASH_REGEX);

      // Verify that the issuer has the correct balances
      const issuerBalancesAfterBurn =
        await issuerWallet.getIssuerTokenBalances();
      const issuerFirstBalanceAfterBurn = issuerBalancesAfterBurn.find(
        (b) => b.tokenIdentifier === firstTokenIdentifier,
      );
      const issuerSecondBalanceAfterBurn = issuerBalancesAfterBurn.find(
        (b) => b.tokenIdentifier === secondTokenIdentifier,
      );
      expect(issuerFirstBalanceAfterBurn?.balance).toBe(0n);
      expect(issuerSecondBalanceAfterBurn?.balance).toEqual(TOKEN_AMOUNT);
    });
  },
);
