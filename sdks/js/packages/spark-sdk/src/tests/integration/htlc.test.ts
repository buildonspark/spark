import { BitcoinFaucet, createNewTree } from "../test-utils.js";
import { type ConfigOptions } from "../../services/wallet-config.js";
import { DefaultSparkSigner, KeyDerivation, KeyDerivationType } from "../../index-shared.js";
import { SparkWalletTestingIntegration } from "../utils/spark-testing-wallet.js";
import { uuidv7 } from "uuidv7";
import { LeafKeyTweak } from "../../services/transfer.js";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { sha256 } from "@noble/hashes/sha2";

test("htlc", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const options: ConfigOptions = {
        network: "LOCAL",
    };

    const { wallet: senderWallet } =
        await SparkWalletTestingIntegration.initialize({
          options,
          signer: new DefaultSparkSigner(),
        });
    
    const senderTransferService = senderWallet.getTransferService();

    const leafId = uuidv7();
    const rootNode = await createNewTree(senderWallet, leafId, faucet, 1000n);

    const newLeafDerivationPath: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: uuidv7(),
    };

    const { wallet: receiverWallet } =
      await SparkWalletTestingIntegration.initialize({
        options,
        signer: new DefaultSparkSigner(),
      });
    const receiverPubkey = await receiverWallet.getIdentityPublicKey();

    const receiverTransferService = receiverWallet.getTransferService();

    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leafId,
      },
      newKeyDerivation: newLeafDerivationPath,
    };

    const preimage = senderWallet.generateRandomPreimage();
    const paymentHash = sha256(preimage);

    const leavesToSend = [transferNode];

    const senderTransfer = await senderWallet.createHTLC(
        leavesToSend,
        hexToBytes(receiverPubkey),
        paymentHash
    );

    const htlcs =
      await receiverWallet.queryHTLCs([paymentHash]);
    expect(htlcs.length).toBe(1);
    expect(htlcs[0]!.transfer!.id).toBe(senderTransfer.id);

    await receiverWallet.claimHTLC(preimage);

    await senderTransferService.deliverTransferPackage(
        senderTransfer,
        leavesToSend,
        new Map(),
        new Map(),
        new Map(),
    );

    const pendingTransfer = await receiverWallet.queryPendingTransfers();
    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];
    expect(receiverTransfer!.id).toBe(senderTransfer.id);

    const leafPrivKeyMap = await receiverWallet.verifyPendingTransfer(
      receiverTransfer!,
    );

    expect(leafPrivKeyMap.size).toBe(1);

    const leafPrivKeyMapBytes = leafPrivKeyMap.get(rootNode.id);
    expect(leafPrivKeyMapBytes).toBeDefined();
    expect(bytesToHex(leafPrivKeyMapBytes!)).toBe(
      bytesToHex(
        await senderWallet
          .getSigner()
          .getPublicKeyFromDerivation(newLeafDerivationPath),
      ),
    );

    const claimingNodes: LeafKeyTweak[] = receiverTransfer!.leaves.map(
      (leaf) => ({
        leaf: leaf.leaf!,
        keyDerivation: {
          type: KeyDerivationType.ECIES,
          path: leaf.secretCipher,
        },
        newKeyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.leaf!.id,
        },
      }),
    );

    await receiverTransferService.claimTransfer(
      receiverTransfer!,
      claimingNodes,
    );

    const balance = await receiverWallet.getBalance();
    expect(balance.balance).toBe(1000n);
});
