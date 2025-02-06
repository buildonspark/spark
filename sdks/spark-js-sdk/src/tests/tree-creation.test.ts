import { bytesToHex } from "@noble/curves/abstract/utils";
import { ConnectionManager } from "../services/connection";
import { SparkWallet } from "../spark-sdk";
import { getTxFromRawTxBytes, getTxId } from "../utils/bitcoin";
import { Network } from "../utils/network";
import { createDummyTx } from "../utils/wasm";

describe("Tree Creation", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "test tree creation address generation",
    async () => {
      const wallet = new SparkWallet(Network.REGTEST);
      const mnemonic = wallet.generateMnemonic();
      await wallet.createSparkWallet(mnemonic);
      const config = wallet.getConfig();

      const mockClient = ConnectionManager.createMockClient(
        config.signingOperators[config.coodinatorIdentifier].address
      );

      const pubKey = wallet.getSigner().generatePublicKey();

      const depositResp = await wallet.generateDepositAddress(pubKey);

      expect(depositResp.depositAddress).toBeDefined();

      const dummyTx = createDummyTx({
        address: depositResp.depositAddress!.address,
        amountSats: 65536n,
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

      const treeResp = await wallet.generateDepositAddressForTree(
        vout,
        pubKey,
        depositTx
      );

      const treeNodes = await wallet.createTree(
        vout,
        treeResp,
        true,
        depositTx
      );

      console.log("tree nodes:", treeNodes);
    },
    30000
  );
});
