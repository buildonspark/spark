import { describe, expect } from "@jest/globals";
import { ConfigOptions } from "../../services/wallet-config.js";
import { SparkWallet } from "../../spark-wallet.js";
import { NetworkType } from "../../utils/network.js";

describe("address", () => {
  describe.each([
    "wear cattle behind affair parade error luxury profit just rate arch cigar",
    "logic ripple layer execute smart disease marine hero monster talent crucial unfair horror shadow maze abuse avoid story loop jaguar sphere trap decrease turn",
    "936eda5945550ab384b4fd91fd6024360f6fdf1ecd9a181fb374d07cdbff0985528dc7aff7305da7dab26ce88425f692d4e3bfefbb27e1770b7773bc3c69e7bb",
    "5904c8ec7a0f8748e4f3d82840cb9736857b8feec921ccd7ceba20d47c9e3e2f3050e6beefefe73a2af8740ff4dc203a33771fe680d9e24934f8a2784eda53be",
  ])('.seedOrMnemonic(%s)', (seedOrMnemonic) => {
    test.concurrent.each([
      ["LOCAL", "spl", "bcrt"],
    ])(`.network(%s)`, async (network, sparkAddressPrefix, blockchainAddressPrefix) => {
      const options: ConfigOptions = {
        network: network as NetworkType,
      };
      const { wallet, ...rest } =
        await SparkWallet.initialize({
          mnemonicOrSeed: seedOrMnemonic,
          options,
        });

      expect(wallet).toBeDefined();
      expect(await wallet.getIdentityPublicKey()).toBeDefined();

      const sparkAddress = await wallet.getSparkAddress();
      expect(sparkAddress).toMatch(new RegExp(`^${sparkAddressPrefix}1[a-zA-Z0-9]{62}$`));
      expect(sparkAddress).toEqual(await wallet.getSparkAddress());

      // Make multiple concurrent calls to getDepositAddress
      const depositAddresses = await Promise.all([
        wallet.getDepositAddress(),
        wallet.getDepositAddress(),
        wallet.getDepositAddress(),
        wallet.getDepositAddress(),
        wallet.getDepositAddress(),
      ]);

      // Verify each address is unique and valid
      let addressMap = new Map<string, string>();
      for (const depositAddress of depositAddresses) {
        expect(depositAddress).toMatch(new RegExp(`^${blockchainAddressPrefix}1[a-zA-Z0-9]{59}$`));
        addressMap.set(depositAddress, depositAddress);
      }
      expect(addressMap.size).toBe(5);

      // Create a new wallet with the same seed or mnemonic
      const { wallet: wallet2, ...rest2 } =
        await SparkWallet.initialize({
          mnemonicOrSeed: seedOrMnemonic,
          options,
        });

      expect(await wallet2.getIdentityPublicKey()).toEqual(await wallet.getIdentityPublicKey());
      const sparkAddress2 = await wallet2.getSparkAddress();
      expect(sparkAddress2).toEqual(sparkAddress);

      // New wallet should continue to generate unique addresses
      const depositAddresses2 = await Promise.all([
        wallet2.getDepositAddress(),
        wallet2.getDepositAddress(),
        wallet2.getDepositAddress(),
        wallet2.getDepositAddress(),
        wallet2.getDepositAddress(),
      ]);

      // Verify each address is unique and valid
      for (const depositAddress of depositAddresses2) {
        expect(depositAddress).toMatch(new RegExp(`^${blockchainAddressPrefix}1[a-zA-Z0-9]{59}$`));
        addressMap.set(depositAddress, depositAddress);
      }
      expect(addressMap.size).toBe(10);
    }, 30000);
  }, 30000);
});
