import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { SparkWallet } from "../spark-sdk";
import { createNewTree } from "./test-util";

describe("Transfer", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn(
    "test transfer",
    async () => {
      const senderWallet = new SparkWallet();
      const senderMnemonic = senderWallet.generateMnemonic();
      await senderWallet.createSparkWallet(senderMnemonic);

      const leafPrivKey = secp256k1.utils.randomPrivateKey();
      const rootNode = await createNewTree(senderWallet, leafPrivKey);

      const newLeafPrivKey = secp256k1.utils.randomPrivateKey();

      const receiverWallet = new SparkWallet();
      const receiverMnemonic = receiverWallet.generateMnemonic();
      const receiverPubkey = await receiverWallet.createSparkWallet(
        receiverMnemonic
      );

      const transferNode = {
        leaf: rootNode,
        signingPrivKey: leafPrivKey,
        newSigningPrivKey: newLeafPrivKey,
      };

      const senderTransfer = await senderWallet.sendTransfer(
        [transferNode],
        hexToBytes(receiverPubkey),
        new Date(Date.now() + 10 * 60 * 1000)
      );

      const pendingTransfer = await receiverWallet.queryPendingTransfers();

      expect(pendingTransfer.transfers.length).toBe(1);

      const receiverTransfer = pendingTransfer.transfers[0];

      expect(receiverTransfer.id).toBe(senderTransfer.id);

      const leafPrivKeyMap = await receiverWallet.verifyPendingTransfer(
        receiverTransfer
      );

      expect(leafPrivKeyMap.size).toBe(1);

      const leafPrivKeyMapBytes = leafPrivKeyMap.get(rootNode.id);
      expect(leafPrivKeyMapBytes).toBeDefined();
      expect(bytesToHex(leafPrivKeyMapBytes!)).toBe(bytesToHex(newLeafPrivKey));

      const finalLeafPrivKey = secp256k1.utils.randomPrivateKey();

      const claimingNode = {
        leaf: rootNode,
        signingPrivKey: newLeafPrivKey,
        newSigningPrivKey: finalLeafPrivKey,
      };

      await receiverWallet.claimTransfer(receiverTransfer, [claimingNode]);
    },
    30000
  );
});
