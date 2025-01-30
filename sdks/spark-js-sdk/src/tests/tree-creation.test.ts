import { SparkWallet } from "../spark-sdk";
import { getTestWalletConfig } from "./test-util";
import { secp256k1 } from "@noble/curves/secp256k1";
import { createDummyTx } from "../utils/wasm";
import { getTxFromRawTxBytes } from "../utils/bitcoin";
import { getTxId } from "../utils/bitcoin";
import { bytesToHex } from "@noble/curves/abstract/utils";
import { ConnectionManager } from "../services/connection";

describe("Tree Creation", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "test tree creation address generation",
    async () => {
      const config = getTestWalletConfig();
      const wallet = new SparkWallet(config);

      const mockClient = ConnectionManager.createMockClient(
        config.signingOperators[config.coodinatorIdentifier].address
      );

      const privKey = secp256k1.utils.randomPrivateKey();
      const pubKey = secp256k1.getPublicKey(privKey);

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
        privKey,
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
