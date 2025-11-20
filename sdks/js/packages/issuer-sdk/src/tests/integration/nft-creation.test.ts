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
