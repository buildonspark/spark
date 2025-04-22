import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { getP2TRAddressFromPublicKey, getTxId } from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { ValidationError, RPCError } from "../../errors/types.js";

const brokenTestFn = process.env.GITHUB_ACTIONS ? it.skip : it;

describe("deposit", () => {
  it("should generate a deposit address", async () => {
    const mnemonic =
      "raise benefit echo client clutch short pyramid grass fall core slogan boil device plastic drastic discover decide penalty middle appear medal elbow original income";
    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    const depositAddress = await sdk.getSingleUseDepositAddress();

    expect(depositAddress).toBeDefined();
  }, 30000);

  it("should create a tree root", async () => {
    const faucet = new BitcoinFaucet();

    const coin = await faucet.fund();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });

    // Generate private/public key pair
    const pubKey = await sdk.getSigner().generatePublicKey();

    // Generate deposit address
    const depositResp = await sdk.getSingleUseDepositAddress();
    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const addr = Address(getNetwork(Network.LOCAL)).decode(depositResp);
    const script = OutScript.encode(addr);

    const depositTx = new Transaction();
    depositTx.addInput(coin!.outpoint);
    depositTx.addOutput({
      script,
      amount: 100_000n,
    });

    const vout = 0;
    const txid = getTxId(depositTx);
    if (!txid) {
      throw new ValidationError("Transaction ID not found", {
        field: "txid",
        value: depositTx,
      });
    }

    // Set mock transaction
    const signedTx = await faucet.signFaucetCoin(
      depositTx,
      coin!.txout,
      coin!.key,
    );

    await faucet.broadcastTx(signedTx.hex);

    const randomPrivKey = secp256k1.utils.randomPrivateKey();
    const randomPubKey = secp256k1.getPublicKey(randomPrivKey);
    const randomAddr = getP2TRAddressFromPublicKey(randomPubKey, Network.LOCAL);

    await faucet.generateToAddress(1, randomAddr);

    // Create tree root
    const treeResp = await sdk.claimDeposit(signedTx.id);

    console.log("tree created:", treeResp);
  }, 30000);

  it("should handle non-trusty deposit", async () => {
    const faucet = new BitcoinFaucet();

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
    const faucet = new BitcoinFaucet();

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
