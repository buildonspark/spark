import { describe, expect, it } from "@jest/globals";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { RPCError } from "../../errors/types.js";
import { getTxId } from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
describe("deposit", () => {
  it("should generate a deposit address", async () => {
    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const depositAddress = await sdk.getSingleUseDepositAddress();

    expect(depositAddress).toBeDefined();
  }, 30000);

  it("should create a tree root", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const depositResp = await sdk.getSingleUseDepositAddress();
    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 100_000n);

    await sdk.claimDeposit(signedTx.id);
  }, 30000);

  it("should restart wallet and recover signing private key", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk, mnemonic } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    // Generate deposit address
    const depositResp = await sdk.getSingleUseDepositAddress();
    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 100_000n);
    await faucet.mineBlocks(6);

    const { wallet: newSdk } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: mnemonic,
      options: {
        network: "LOCAL",
      },
    });

    await newSdk.claimDeposit(signedTx.id);
  }, 30000);

  it("should handle non-trusty deposit", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const coin = await faucet.fund();

    const depositTx = new Transaction();
    const sendAmount = 50_000n;

    depositTx.addInput(coin!.outpoint);

    const depositAddress = await sdk.getSingleUseDepositAddress();
    if (!depositAddress) {
      throw new Error("Failed to get deposit address");
    }

    const destinationAddress = Address(getNetwork(Network.LOCAL)).decode(
      depositAddress,
    );
    const destinationScript = OutScript.encode(destinationAddress);
    depositTx.addOutput({
      script: destinationScript,
      amount: sendAmount,
    });

    const unsignedTxHex = depositTx.hex;

    const depositResult = await sdk.advancedDeposit(unsignedTxHex);
    expect(depositResult).toBeDefined();

    const signedTx = await faucet.signFaucetCoin(
      depositTx,
      coin!.txout,
      coin!.key,
    );

    const broadcastResult = await faucet.broadcastTx(signedTx.hex);
    expect(broadcastResult).toBeDefined();

    await faucet.generateToAddress(1, depositAddress);

    // Sleep to allow chain watcher to catch up
    await new Promise((resolve) => setTimeout(resolve, 3000));

    const balance = await sdk.getBalance();
    expect(balance.balance).toEqual(sendAmount);

    await expect(sdk.advancedDeposit(unsignedTxHex)).rejects.toThrow(
      `No unused deposit address found for tx: ${getTxId(depositTx)}`,
    );
  }, 30000);

  it("should handle single tx with multiple outputs to unused deposit addresses", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const coin = await faucet.fund();

    const depositTx = new Transaction();
    const sendAmount = 50_000n;

    depositTx.addInput(coin!.outpoint);

    const depositAddress = await sdk.getSingleUseDepositAddress();
    if (!depositAddress) {
      throw new Error("Failed to get deposit address");
    }

    const depositAddress2 = await sdk.getSingleUseDepositAddress();
    if (!depositAddress2) {
      throw new Error("Failed to get deposit address");
    }

    const destinationAddress = Address(getNetwork(Network.LOCAL)).decode(
      depositAddress,
    );
    const destinationScript = OutScript.encode(destinationAddress);
    depositTx.addOutput({
      script: destinationScript,
      amount: sendAmount,
    });

    const destinationAddress2 = Address(getNetwork(Network.LOCAL)).decode(
      depositAddress2,
    );
    const destinationScript2 = OutScript.encode(destinationAddress2);
    depositTx.addOutput({
      script: destinationScript2,
      amount: sendAmount,
    });

    const unsignedTxHex = depositTx.hex;

    const depositResult = await sdk.advancedDeposit(unsignedTxHex);
    expect(depositResult).toBeDefined();

    const signedTx = await faucet.signFaucetCoin(
      depositTx,
      coin!.txout,
      coin!.key,
    );

    const broadcastResult = await faucet.broadcastTx(signedTx.hex);
    expect(broadcastResult).toBeDefined();

    await faucet.generateToAddress(1, depositAddress);

    // Sleep to allow chain watcher to catch up
    await new Promise((resolve) => setTimeout(resolve, 3000));

    const balance = await sdk.getBalance();
    expect(balance.balance).toEqual(sendAmount * 2n);
  }, 30000);
});
