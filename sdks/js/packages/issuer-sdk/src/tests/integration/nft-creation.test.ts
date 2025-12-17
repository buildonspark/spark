import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import { TEST_CONFIGS } from "./test-configs.js";
import { MAX_TOKEN_CONTENT_SIZE } from "../../utils/create-validation.js";

describe.each(TEST_CONFIGS)(
  "nft creation tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should create an nft", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const tokenName = `${name}NFT`;
      const tokenTicker = "NFT";
      const extraMetadata = new Uint8Array([1, 2, 3]);
      const createTransactionDetails = await issuerWallet.createToken({
        tokenName,
        tokenTicker,
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
        extraMetadata,
        returnIdentifierForCreate: true,
      });

      expect(typeof createTransactionDetails).toBe("object");
      expect(createTransactionDetails.tokenIdentifier).toBeDefined();
      expect(createTransactionDetails.tokenIdentifier.length).toBeGreaterThan(
        0,
      );
      expect(createTransactionDetails.transactionHash).toBeDefined();
      expect(createTransactionDetails.transactionHash.length).toBeGreaterThan(
        0,
      );
      const bech32mTokenIdentifier = createTransactionDetails.tokenIdentifier;

      const metadata = await issuerWallet.getIssuerTokensMetadata();
      expect(metadata.length).toEqual(1);
      const tokensMetadata = metadata[0];
      expect(tokensMetadata.tokenName).toEqual(tokenName);
      expect(tokensMetadata.tokenTicker).toEqual(tokenTicker);
      expect(tokensMetadata.maxSupply).toEqual(1n);
      expect(tokensMetadata.decimals).toEqual(0);
      expect(Array.from(tokensMetadata.extraMetadata!)).toEqual(
        Array.from(extraMetadata),
      );

      const txId = await issuerWallet.mintTokens({
        tokenAmount: 1n,
        tokenIdentifier: bech32mTokenIdentifier,
      });
      expect(typeof txId).toBe("string");
      expect(txId.length).toBeGreaterThan(0);

      const tokenIdentifiers = await issuerWallet.getIssuerTokenIdentifiers();
      expect(tokenIdentifiers.length).toEqual(1);
      expect(tokenIdentifiers[0]).toEqual(bech32mTokenIdentifier);

      const { balance: satsBalance, tokenBalances: tokenBalancesMap } =
        await issuerWallet.getBalance();
      expect(satsBalance).toEqual(0n);
      expect(tokenBalancesMap.size).toEqual(1);
      expect(tokenBalancesMap.get(bech32mTokenIdentifier)).toBeDefined();
      expect(tokenBalancesMap.get(bech32mTokenIdentifier)?.balance).toEqual(1n);

      const tokenMetadata = tokenBalancesMap.get(
        bech32mTokenIdentifier,
      )?.tokenMetadata;
      expect(tokenMetadata).toBeDefined();
      expect(tokenMetadata?.tokenName).toEqual(tokenName);
      expect(tokenMetadata?.tokenTicker).toEqual(tokenTicker);
      expect(tokenMetadata?.maxSupply).toEqual(1n);
      expect(tokenMetadata?.decimals).toEqual(0);
      expect(tokenMetadata?.extraMetadata).toEqual(extraMetadata);
    });

    it("should fail to create an nft with extra metadata longer than MAX_TOKEN_CONTENT_SIZE bytes", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const extraMetadata = new Uint8Array(MAX_TOKEN_CONTENT_SIZE + 1);

      await expect(
        issuerWallet.createToken({
          tokenName: "NFTLONGMETADATA",
          tokenTicker: "NFTLONG",
          decimals: 0,
          isFreezable: false,
          maxSupply: 1n,
          extraMetadata,
        }),
      ).rejects.toThrow();
    });
  },
);
