import { describe, expect, it } from "@jest/globals";
import {
  bytesToHex,
  equalBytes,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { sha256 } from "@scure/btc-signer/utils";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { LeafKeyTweak, TransferService } from "../../services/transfer.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import { createNewTree } from "../../tests/test-util.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

describe("Transfer", () => {
  it(
    "test transfer",
    async () => {
      const faucet = new BitcoinFaucet();

      const options: ConfigOptions = {
        network: "LOCAL",
      };

      const { wallet: senderWallet } = await SparkWalletTesting.initialize({
        options,
      });

      const senderConfigService = new WalletConfigService(
        options,
        senderWallet.getSigner(),
      );
      const senderConnectionManager = new ConnectionManager(
        senderConfigService,
      );
      const senderTransferService = new TransferService(
        senderConfigService,
        senderConnectionManager,
      );

      const leafPubKey = await senderWallet.getSigner().generatePublicKey();
      const rootNode = await createNewTree(
        senderWallet,
        leafPubKey,
        faucet,
        1000n,
      );

      const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

      const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
        options,
      });
      const receiverPubkey = await receiverWallet.getIdentityPublicKey();

      const receiverConfigService = new WalletConfigService(
        options,
        receiverWallet.getSigner(),
      );
      const receiverConnectionManager = new ConnectionManager(
        receiverConfigService,
      );

      const receiverTransferService = new TransferService(
        receiverConfigService,
        receiverConnectionManager,
      );

      const transferNode = {
        leaf: rootNode,
        signingPubKey: leafPubKey,
        newSigningPubKey: newLeafPubKey,
      };

      const senderTransfer = await senderTransferService.sendTransfer(
        [transferNode],
        hexToBytes(receiverPubkey),
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
      expect(bytesToHex(leafPrivKeyMapBytes!)).toBe(bytesToHex(newLeafPubKey));

      const finalLeafPubKey = await receiverWallet
        .getSigner()
        .generatePublicKey(sha256(rootNode.id));

      const claimingNode = {
        leaf: rootNode,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      };

      await receiverTransferService.claimTransfer(receiverTransfer!, [
        claimingNode,
      ]);

      const { wallet: newReceiverWallet } = await SparkWalletTesting.initialize(
        {
          options: {
            network: "LOCAL",
          },
        },
      );
      const newReceiverPubkey = await newReceiverWallet.getSparkAddress();

      await receiverWallet.transfer({
        amountSats: 1000,
        receiverSparkAddress: newReceiverPubkey,
      });

      const newPendingTransfer =
        await newReceiverWallet.queryPendingTransfers();

      expect(newPendingTransfer.transfers.length).toBe(1);
      await newReceiverWallet.getBalance();
    },
    30000,
  );

  it("test transfer with separate", async () => {
    const faucet = new BitcoinFaucet();

    const options: ConfigOptions = {
      network: "LOCAL",
    };
    const { wallet: senderWallet } = await SparkWalletTesting.initialize({
      options,
    });

    const senderConfigService = new WalletConfigService(
      options,
      senderWallet.getSigner(),
    );
    const senderConnectionManager = new ConnectionManager(senderConfigService);
    const senderTransferService = new TransferService(
      senderConfigService,
      senderConnectionManager,
    );

    const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
      options,
    });
    const receiverPubkey = await receiverWallet.getIdentityPublicKey();

    const receiverConfigService = new WalletConfigService(
      options,
      receiverWallet.getSigner(),
    );
    const receiverConnectionManager = new ConnectionManager(
      receiverConfigService,
    );
    const receiverTransferService = new TransferService(
      receiverConfigService,
      receiverConnectionManager,
    );

    const leafPubKey = await senderWallet.getSigner().generatePublicKey();

    const rootNode = await createNewTree(
      senderWallet,
      leafPubKey,
      faucet,
      100_000n,
    );

    const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      signingPubKey: leafPubKey,
      newSigningPubKey: newLeafPubKey,
    };

    const leavesToTransfer = [transferNode];

    const senderTransfer = await senderTransferService.sendTransfer(
      leavesToTransfer,
      hexToBytes(receiverPubkey),
    );

    // Receiver queries pending transfer
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
    expect(equalBytes(leafPrivKeyMapBytes!, newLeafPubKey)).toBe(true);

    const finalLeafPubKey = await receiverWallet
      .getSigner()
      .generatePublicKey(sha256(rootNode.id));

    const claimingNode: LeafKeyTweak = {
      leaf: receiverTransfer!.leaves[0]!.leaf!,
      signingPubKey: newLeafPubKey,
      newSigningPubKey: finalLeafPubKey,
    };

    const transferService = new TransferService(
      receiverConfigService,
      new ConnectionManager(receiverConfigService),
    );

    await transferService.claimTransferTweakKeys(receiverTransfer!, [
      claimingNode,
    ]);

    const newPendingTransfer = await receiverWallet.queryPendingTransfers();

    expect(newPendingTransfer.transfers.length).toBe(1);

    const newReceiverTransfer = newPendingTransfer.transfers[0];
    expect(newReceiverTransfer!.id).toBe(receiverTransfer!.id);

    const newLeafPubKeyMap = await receiverWallet.verifyPendingTransfer(
      newReceiverTransfer!,
    );

    expect(newLeafPubKeyMap.size).toBe(1);

    const newLeafPubKeyMapBytes = newLeafPubKeyMap.get(rootNode.id);
    expect(newLeafPubKeyMapBytes).toBeDefined();
    expect(bytesToHex(newLeafPubKeyMapBytes!)).toBe(bytesToHex(newLeafPubKey));

    await transferService.claimTransferSignRefunds(newReceiverTransfer!, [
      claimingNode,
    ]);

    const newNewPendingTransfer = await receiverWallet.queryPendingTransfers();
    expect(newNewPendingTransfer.transfers.length).toBe(1);

    await receiverTransferService.claimTransfer(
      newNewPendingTransfer.transfers[0]!,
      [claimingNode],
    );
  });

  it("cancel transfer", async () => {
    const faucet = new BitcoinFaucet();

    const options: ConfigOptions = {
      network: "LOCAL",
    };
    const { wallet: senderWallet } = await SparkWalletTesting.initialize({
      options,
    });
    const mnemonic = generateMnemonic(wordlist);

    const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
      options,
    });
    const receiverPubkey = await receiverWallet.getIdentityPublicKey();

    const receiverConfigService = new WalletConfigService(
      options,
      receiverWallet.getSigner(),
    );
    const receiverConnectionManager = new ConnectionManager(
      receiverConfigService,
    );
    const receiverTransferService = new TransferService(
      receiverConfigService,
      receiverConnectionManager,
    );

    const leafPubKey = await senderWallet.getSigner().generatePublicKey();
    const rootNode = await createNewTree(
      senderWallet,
      leafPubKey,
      faucet,
      100_000n,
    );

    const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      signingPubKey: leafPubKey,
      newSigningPubKey: newLeafPubKey,
    };

    const senderConfigService = new WalletConfigService(
      options,
      senderWallet.getSigner(),
    );
    const senderConnectionManager = new ConnectionManager(senderConfigService);
    const senderTransferService = new TransferService(
      senderConfigService,
      senderConnectionManager,
    );

    const senderTransfer = await senderTransferService.sendTransferSignRefund(
      [transferNode],
      hexToBytes(receiverPubkey),
      new Date(Date.now() + 10 * 60 * 1000),
    );

    await senderTransferService.cancelSendTransfer(
      senderTransfer.transfer,
      senderConfigService.getCoordinatorAddress(),
    );

    const newSenderTransfer = await senderTransferService.sendTransfer(
      [transferNode],
      hexToBytes(receiverPubkey),
    );

    const pendingTransfer = await receiverWallet.queryPendingTransfers();
    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];
    expect(receiverTransfer!.id).toBe(newSenderTransfer.id);

    const leafPubKeyMap = await receiverWallet.verifyPendingTransfer(
      receiverTransfer!,
    );

    expect(leafPubKeyMap.size).toBe(1);

    const leafPubKeyMapBytes = leafPubKeyMap.get(rootNode.id);
    expect(leafPubKeyMapBytes).toBeDefined();
    expect(equalBytes(leafPubKeyMapBytes!, newLeafPubKey)).toBe(true);

    const finalLeafPubKey = await receiverWallet
      .getSigner()
      .generatePublicKey(sha256(rootNode.id));

    const claimingNode: LeafKeyTweak = {
      leaf: receiverTransfer!.leaves[0]!.leaf!,
      signingPubKey: newLeafPubKey,
      newSigningPubKey: finalLeafPubKey,
    };

    await receiverTransferService.claimTransfer(receiverTransfer!, [
      claimingNode,
    ]);
  });
});
