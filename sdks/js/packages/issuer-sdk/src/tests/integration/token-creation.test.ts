import { ConfigOptions, WalletConfig } from "@buildonspark/spark-sdk";
import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";

export const TOKENS_SCHNORR_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "SCHNORR",
};

export const TOKENS_ECDSA_CONFIG: Required<ConfigOptions> = {
  ...WalletConfig.LOCAL,
  tokenSignatures: "ECDSA",
};

const TEST_CONFIGS = [
  { name: "TE", config: TOKENS_ECDSA_CONFIG },
  { name: "TS", config: TOKENS_SCHNORR_CONFIG },
];

describe.each(TEST_CONFIGS)(
  "token creation tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should create a token", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const tokenName = `${name}Creatable`;
      const tokenTicker = "CRT";
      const maxSupply = 5000n;
      const decimals = 0;
      const txId = await issuerWallet.createToken({
        tokenName,
        tokenTicker,
        decimals,
        isFreezable: false,
        maxSupply,
      });

      expect(typeof txId).toBe("string");
      expect(txId.length).toBeGreaterThan(0);

      const metadata = await issuerWallet.getIssuerTokenMetadata();
      expect(metadata.tokenName).toEqual(tokenName);
      expect(metadata.tokenTicker).toEqual(tokenTicker);
      expect(metadata.maxSupply).toEqual(maxSupply);
      expect(metadata.decimals).toEqual(decimals);
    });

    it("should fail on duplicate token creation", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      const tokenName = `${name}Dup`;
      const tokenTicker = `DP${name}`;

      await issuerWallet.createToken({
        tokenName,
        tokenTicker,
        decimals: 0,
        isFreezable: false,
        maxSupply: 100n,
      });

      await expect(
        issuerWallet.createToken({
          tokenName,
          tokenTicker,
          decimals: 0,
          isFreezable: false,
          maxSupply: 100n,
        }),
      ).rejects.toThrow();
    });

    it("should be able to create a token with name of size equal to MAX_SYMBOL_SIZE", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: "MST",
        tokenTicker: "TESTAA",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
      });
    });

    it("should be able to create a token with symbol of size equal to MAX_NAME_SIZE", async () => {
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: "ABCDEFGHIJKLMNOPQ",
        tokenTicker: "MQS",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
      });
    });
  },
);
