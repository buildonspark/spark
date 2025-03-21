import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { getP2TRAddressFromPublicKey, getTxId } from "../utils/bitcoin.js";
import { getNetwork, Network } from "../utils/network.js";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";

describe("deposit", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "should generate a deposit address",
    async () => {
      const mnemonic =
        "raise benefit echo client clutch short pyramid grass fall core slogan boil device plastic drastic discover decide penalty middle appear medal elbow original income";
      const { wallet: sdk } = await SparkWalletTesting.initialize({
        options: {
          network: "LOCAL",
        },
      });

      const depositAddress = await sdk.getDepositAddress();

      expect(depositAddress).toBeDefined();
    },
    30000,
  );

  testFn(
    "should create a tree root",
    async () => {
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
      const depositResp = await sdk.getDepositAddress();
      if (!depositResp) {
        throw new Error("deposit address not found");
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
        throw new Error("txid not found");
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
      const randomAddr = getP2TRAddressFromPublicKey(
        randomPubKey,
        Network.LOCAL,
      );

      await faucet.generateToAddress(1, randomAddr);

      // Create tree root
      const treeResp = await sdk.claimDeposit(signedTx.id);

      console.log("tree created:", treeResp);
    },
    30000,
  );
});
