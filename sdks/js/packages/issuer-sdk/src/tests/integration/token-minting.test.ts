import { jest } from "@jest/globals";
import { IssuerSparkWalletTesting } from "../utils/issuer-test-wallet.js";
import {
  getSingleIssuerTokenBalance,
  getSingleIssuerTokenMetadata,
  mintSingleIssuerToken,
} from "../utils/multi-token-utils.js";
import { TEST_CONFIGS } from "./test-configs.js";

describe.each(TEST_CONFIGS)(
  "token minting tests - $name",
  ({ name, config }) => {
    jest.setTimeout(80000);

    it("should fail when minting tokens without creation", async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet } = await IssuerSparkWalletTesting.initialize({
        options: config,
      });

      await expect(
        mintSingleIssuerToken(wallet, tokenAmount),
      ).rejects.toThrow();
    });

    it("should create, and fail when minting more than max supply", async () => {
      const tokenAmount: bigint = 1000n;
      const { wallet } = await IssuerSparkWalletTesting.initialize({
        options: config,
      });

      await wallet.createToken({
        tokenName: "MST",
        tokenTicker: "MST",
        decimals: 0,
        isFreezable: false,
        maxSupply: 2n,
      });
      await expect(
        mintSingleIssuerToken(wallet, tokenAmount),
      ).rejects.toThrow();
    });

    it("should create, and mint tokens successfully", async () => {
      const tokenAmount: bigint = 1000n;

      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });
      await issuerWallet.createToken({
        tokenName: `${name}M`,
        tokenTicker: "MIN",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1_000_000n,
      });

      const tokenMetadata = await getSingleIssuerTokenMetadata(issuerWallet);

      const identityPublicKey = await issuerWallet.getIdentityPublicKey();
      expect(tokenMetadata?.tokenName).toEqual(`${name}M`);
      expect(tokenMetadata?.tokenTicker).toEqual("MIN");
      expect(tokenMetadata?.decimals).toEqual(0);
      expect(tokenMetadata?.maxSupply).toEqual(1000000n);
      expect(tokenMetadata?.isFreezable).toEqual(false);

      const metadataPubkey = tokenMetadata?.tokenPublicKey;
      expect(metadataPubkey).toEqual(identityPublicKey);

      await mintSingleIssuerToken(issuerWallet, tokenAmount);

      const tokenBalance = await getSingleIssuerTokenBalance(issuerWallet);
      expect(tokenBalance.balance).toBeGreaterThanOrEqual(tokenAmount);
    });

    it("should mint token with 1 max supply without issue", async () => {
      const tokenAmount: bigint = 1n;
      const { wallet: issuerWallet } =
        await IssuerSparkWalletTesting.initialize({
          options: config,
        });

      await issuerWallet.createToken({
        tokenName: "MST",
        tokenTicker: "MST",
        decimals: 0,
        isFreezable: false,
        maxSupply: 1n,
      });
      await mintSingleIssuerToken(issuerWallet, tokenAmount);

      const tokenBalance = await getSingleIssuerTokenBalance(issuerWallet);
      expect(tokenBalance.balance).toEqual(tokenAmount);
    });
  },
);
