import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { SparkWallet } from "../spark-sdk";
import { getP2TRAddressFromPublicKey, getTxId } from "../utils/bitcoin";
import { getNetwork, Network } from "../utils/network";
import { BitcoinFaucet } from "./utils/test-faucet";

describe("deposit", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "should generate a deposit address",
    async () => {
      const mnemonic =
        "olive hawk cabbage obvious future great grass reunion plunge cereal rate canyon";
      const sdk = new SparkWallet(Network.REGTEST);
      await sdk.createSparkWallet(mnemonic);

      const pubKey = sdk.getSigner().generatePublicKey();

      const depositAddress = await sdk.generateDepositAddress(pubKey);

      expect(depositAddress.depositAddress).toBeDefined();
    },
    30000
  );

  testFn(
    "should create a tree root",
    async () => {
      const faucet = new BitcoinFaucet(
        "http://127.0.0.1:18443",
        "admin1",
        "123"
      );

      const coin = await faucet.fund();

      const sdk = new SparkWallet(Network.REGTEST);
      const mnemonic = sdk.generateMnemonic();
      await sdk.createSparkWallet(mnemonic);
      const config = sdk.getConfig();

      // Generate private/public key pair
      const pubKey = sdk.getSigner().generatePublicKey();

      // Generate deposit address
      const depositResp = await sdk.generateDepositAddress(pubKey);
      if (!depositResp.depositAddress) {
        throw new Error("deposit address not found");
      }

      const addr = Address(getNetwork(Network.REGTEST)).decode(
        depositResp.depositAddress.address
      );
      const script = OutScript.encode(addr);

      const depositTx = new Transaction();
      depositTx.addInput(coin.outpoint);
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
        coin.txout,
        coin.key
      );

      await faucet.broadcastTx(signedTx.hex);

      const randomPrivKey = secp256k1.utils.randomPrivateKey();
      const randomPubKey = secp256k1.getPublicKey(randomPrivKey);
      const randomAddr = getP2TRAddressFromPublicKey(
        randomPubKey,
        Network.REGTEST
      );

      await faucet.generateToAddress(1, randomAddr);

      // Create tree root
      const treeResp = await sdk.createTreeRoot(
        pubKey,
        depositResp.depositAddress.verifyingKey,
        depositTx,
        vout
      );

      console.log("tree created:", treeResp);
    },
    30000
  );
});
