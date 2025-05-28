import { describe, expect, it } from "@jest/globals";
import { ConfigOptions } from "../../services/wallet-config.js";
import { NetworkType } from "../../utils/network.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
describe("wallet", () => {
  it("should initialize a wallet", async () => {
    const seedOrMnemonics = [
      "wear cattle behind affair parade error luxury profit just rate arch cigar",
      "logic ripple layer execute smart disease marine hero monster talent crucial unfair horror shadow maze abuse avoid story loop jaguar sphere trap decrease turn",
      "936eda5945550ab384b4fd91fd6024360f6fdf1ecd9a181fb374d07cdbff0985528dc7aff7305da7dab26ce88425f692d4e3bfefbb27e1770b7773bc3c69e7bb",
      "5904c8ec7a0f8748e4f3d82840cb9736857b8feec921ccd7ceba20d47c9e3e2f3050e6beefefe73a2af8740ff4dc203a33771fe680d9e24934f8a2784eda53be",
    ];
    const networks: NetworkType[] = ["LOCAL"];

    for (const seedOrMnemonic of seedOrMnemonics) {
      for (const network of networks) {
        const options: ConfigOptions = {
          network,
        };
        const { wallet, ...rest } = await SparkWalletTesting.initialize({
          mnemonicOrSeed: seedOrMnemonic,
          options,
        });
        expect(wallet).toBeDefined();
      }
    }
  }, 30000);

  it("should initialize wallets with different identity keys for different account numbers with the same mnemonic", async () => {
    const seedOrMnemonics =
      "wear cattle behind affair parade error luxury profit just rate arch cigar";

    const networks: NetworkType[] = ["LOCAL"];

    const { wallet: accountTen } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: seedOrMnemonics,
      accountNumber: 10,
      options: {
        network: "LOCAL",
      },
    });
    const { wallet: accountEleven } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: seedOrMnemonics,
      accountNumber: 11,
      options: {
        network: "LOCAL",
      },
    });

    const accountTenIdentityKey = await accountTen.getIdentityPublicKey();
    const accountElevenIdentityKey = await accountEleven.getIdentityPublicKey();
    expect(accountTenIdentityKey).not.toEqual(accountElevenIdentityKey);
  });

  it("should not initialize a wallet with an invalid seed or mnemonic", async () => {
    const seedOrMnemonics = [
      "wear cattle behind affair parade error luxury profit just rate arch",
      "jot jot jot jot",
      "936eda5945550ab384b4fd91fd",
      "tb1qzf5a9dwm2gxwkrptsy67xynu4vmr0cvx2zwctg",
    ];

    for (const seedOrMnemonic of seedOrMnemonics) {
      const options: ConfigOptions = {
        network: "LOCAL",
      };
      await expect(
        SparkWalletTesting.initialize({
          mnemonicOrSeed: seedOrMnemonic,
          options,
        }),
      ).rejects.toThrow();
    }
  });
});
