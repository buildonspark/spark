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
      const createTransactionId = await issuerWallet.createToken({
        tokenName,
        tokenTicker,
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
        extraMetadata,
      });

      expect(typeof createTransactionId).toBe("string");
      expect(createTransactionId.length).toBeGreaterThan(0);

      const metadata = await issuerWallet.getIssuerTokenMetadata();
      expect(metadata.tokenName).toEqual(tokenName);
      expect(metadata.tokenTicker).toEqual(tokenTicker);
      expect(metadata.maxSupply).toEqual(1n);
      expect(metadata.decimals).toEqual(0);
      expect(Array.from(metadata.extraMetadata!)).toEqual(
        Array.from(extraMetadata),
      );

      const txId = await issuerWallet.mintTokens(1n);
      expect(typeof txId).toBe("string");
      expect(txId.length).toBeGreaterThan(0);

      const tokenIdentifier = await issuerWallet.getIssuerTokenIdentifier();
      expect(tokenIdentifier).toBeDefined();
      expect(tokenIdentifier!.length).toBeGreaterThan(0);

      const { balance: satsBalance, tokenBalances: tokenBalancesMap } =
        await issuerWallet.getBalance();
      expect(satsBalance).toEqual(0n);
      expect(tokenBalancesMap.size).toEqual(1);
      expect(tokenBalancesMap.get(tokenIdentifier)).toBeDefined();
      expect(tokenBalancesMap.get(tokenIdentifier)?.balance).toEqual(1n);

      const tokenMetadata =
        tokenBalancesMap.get(tokenIdentifier)?.tokenMetadata;
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
