import { bytesToHex } from "@noble/curves/abstract/utils";
import { ConnectionManager } from "../services/connection";
import { SparkWallet } from "../spark-sdk";
import { getTxFromRawTxBytes, getTxId } from "../utils/bitcoin";
import { Network } from "../utils/network";
import { createDummyTx } from "../utils/wasm";

describe("deposit", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "should generate a deposit address",
    async () => {
      const sdk = new SparkWallet(Network.REGTEST);
      const mnemonic = sdk.generateMnemonic();
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
      const sdk = new SparkWallet(Network.REGTEST);
      const mnemonic = sdk.generateMnemonic();
      await sdk.createSparkWallet(mnemonic);
      const config = sdk.getConfig();

      // Setup mock connection
      const mockClient = ConnectionManager.createMockClient(
        config.signingOperators[config.coodinatorIdentifier].address
      );

      // Generate private/public key pair
      const pubKey = sdk.getSigner().generatePublicKey();

      // Generate deposit address
      const depositResp = await sdk.generateDepositAddress(pubKey);
      if (!depositResp.depositAddress) {
        throw new Error("deposit address not found");
      }

      console.log("depositResp", depositResp.depositAddress.address);

      const dummyTx = createDummyTx({
        address: depositResp.depositAddress.address,
        amountSats: 100_000n,
      });

      const depositTxHex = bytesToHex(dummyTx.tx);
      const depositTx = getTxFromRawTxBytes(dummyTx.tx);

      const vout = 0;
      const txid = getTxId(depositTx);
      if (!txid) {
        throw new Error("txid not found");
      }

      // Set mock transaction
      await mockClient.set_mock_onchain_tx({
        txid,
        tx: depositTxHex,
      });

      // Create tree root
      const treeResp = await sdk.createTreeRoot(
        pubKey,
        depositResp.depositAddress.verifyingKey,
        depositTx,
        vout
      );

      console.log("tree created:", treeResp);

      mockClient.close();
    },
    30000
  );
});
