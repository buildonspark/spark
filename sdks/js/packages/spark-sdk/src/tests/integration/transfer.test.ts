import { describe, expect, it, jest } from "@jest/globals";
import {
  bytesToHex,
  equalBytes,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { sha256 } from "@scure/btc-signer/utils";
import { TransferStatus } from "../../proto/spark.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { LeafKeyTweak, TransferService } from "../../services/transfer.js";
import {
  ConfigOptions,
  getLocalSigningOperators,
  LOCAL_WALLET_CONFIG,
} from "../../services/wallet-config.js";
import { createNewTree } from "../../tests/test-util.js";
import { NetworkType } from "../../utils/network.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

describe("Transfer", () => {
  jest.setTimeout(15_000);
  it("test transfer", async () => {
    const faucet = BitcoinFaucet.getInstance();

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

    const { wallet: newReceiverWallet } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
    });
    const newReceiverPubkey = await newReceiverWallet.getSparkAddress();

    await receiverWallet.transfer({
      amountSats: 1000,
      receiverSparkAddress: newReceiverPubkey,
    });

    const newPendingTransfer = await newReceiverWallet.queryPendingTransfers();

    expect(newPendingTransfer.transfers.length).toBe(1);
    await newReceiverWallet.getBalance();
  }, 30000);

  it("test transfer with separate", async () => {
    const faucet = BitcoinFaucet.getInstance();

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
    const faucet = BitcoinFaucet.getInstance();

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

    await senderTransferService.cancelTransfer(
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

  it("test that when the receiver has tweaked the key on some SOs, we can still claim the transfer", async () => {
    const faucet = BitcoinFaucet.getInstance();

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

    const leafPubKey = await senderWallet.getSigner().generatePublicKey();
    const rootNode = await createNewTree(
      senderWallet,
      leafPubKey,
      faucet,
      1000n,
    );

    const newLeafPubKey = await senderWallet.getSigner().generatePublicKey();

    const soToRemove =
      "0000000000000000000000000000000000000000000000000000000000000005";
    const localSigningOperators = getLocalSigningOperators();
    const signingOperators = Object.fromEntries(
      Object.entries(localSigningOperators).filter(
        ([key]) => key !== soToRemove,
      ),
    );
    const missingOperatorOptions = {
      ...LOCAL_WALLET_CONFIG,
      signingOperators,
    };
    const mnemonic = generateMnemonic(wordlist);
    const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
      options: missingOperatorOptions,
      mnemonicOrSeed: mnemonic,
    });

    const receiverPubkey = await receiverWallet.getIdentityPublicKey();

    const receiverConfigService = new WalletConfigService(
      missingOperatorOptions,
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

    // Tweak the key with only 4 out of the 5 operators
    await receiverTransferService.claimTransferTweakKeys(receiverTransfer!, [
      claimingNode,
    ]);

    const receiverOptions = {
      ...LOCAL_WALLET_CONFIG,
    };

    const { wallet: receiverWalletWithAllOperators } =
      await SparkWalletTesting.initialize({
        options: receiverOptions,
        mnemonicOrSeed: mnemonic,
      });
    const receiverConfigServiceWithAllOperators = new WalletConfigService(
      receiverOptions,
      receiverWalletWithAllOperators.getSigner(),
    );
    const receiverConnectionManagerWithAllOperators = new ConnectionManager(
      receiverConfigServiceWithAllOperators,
    );

    const receiverTransferServiceWithAllOperators = new TransferService(
      receiverConfigServiceWithAllOperators,
      receiverConnectionManagerWithAllOperators,
    );

    const { wallet: receiverWalletWithMissingOperatorAsCoordinator } =
      await SparkWalletTesting.initialize({
        options: {
          ...LOCAL_WALLET_CONFIG,
          coodinatorIdentifier: soToRemove,
        },
        mnemonicOrSeed: mnemonic,
      });

    const pendingTransferWithMissingOperatorAsCoordinator =
      await receiverWalletWithMissingOperatorAsCoordinator.queryPendingTransfers();

    expect(
      pendingTransferWithMissingOperatorAsCoordinator.transfers.length,
    ).toBe(1);
    expect(
      pendingTransferWithMissingOperatorAsCoordinator.transfers[0]!.status,
    ).toBe(TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED);

    const pendingTransferWithAllOperators =
      await receiverWalletWithAllOperators.queryPendingTransfers();

    expect(pendingTransferWithAllOperators.transfers.length).toBe(1);
    expect(pendingTransferWithAllOperators.transfers[0]!.status).toBe(
      TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
    );

    const receiverTransferWithAllOperators =
      pendingTransferWithAllOperators.transfers[0];

    expect(receiverTransferWithAllOperators!.id).toBe(senderTransfer.id);

    const leafPrivKeyMapWithAllOperators =
      await receiverWalletWithAllOperators.verifyPendingTransfer(
        receiverTransferWithAllOperators!,
      );

    expect(leafPrivKeyMapWithAllOperators.size).toBe(1);

    const leafPrivKeyMapBytesWithAllOperators =
      leafPrivKeyMapWithAllOperators.get(rootNode.id);
    expect(leafPrivKeyMapBytesWithAllOperators).toBeDefined();
    expect(bytesToHex(leafPrivKeyMapBytesWithAllOperators!)).toBe(
      bytesToHex(newLeafPubKey),
    );

    await receiverWalletWithAllOperators
      .getSigner()
      .restoreSigningKeysFromLeafs([claimingNode.leaf]);

    await receiverWalletWithAllOperators.verifyPendingTransfer(
      receiverTransfer!,
    );

    await receiverTransferServiceWithAllOperators.claimTransfer(
      receiverTransfer!,
      [claimingNode],
    );
  });

  function generateNetworkPairs(
    networks: NetworkType[],
  ): [NetworkType, NetworkType][] {
    const pairs: [NetworkType, NetworkType][] = [];
    for (const source of networks) {
      for (const target of networks) {
        if (source !== target) {
          pairs.push([source, target]);
        }
      }
    }
    return pairs;
  }

  describe("address validation", () => {
    const networkTypes: NetworkType[] = [
      "MAINNET",
      "TESTNET",
      "REGTEST",
      "SIGNET",
      "LOCAL",
    ];
    const networkCombinations = generateNetworkPairs(networkTypes);

    // it.concurrent.each(networkCombinations)(
    //   "should not allow transfer from %s to %s network due to address validation",
    //   async (sourceNetwork, targetNetwork) => {
    //     const sourceOptions: ConfigOptions = {
    //       network: sourceNetwork,
    //     };
    //     const targetOptions: ConfigOptions = {
    //       network: targetNetwork,
    //     };

    //     const { wallet: sourceWallet } = await SparkWalletTesting.initialize({
    //       options: sourceOptions,
    //     });

    //     const { wallet: targetWallet } = await SparkWalletTesting.initialize({
    //       options: targetOptions,
    //     });

    //     const targetAddress = await targetWallet.getSparkAddress();

    //     await expect(
    //       sourceWallet.transfer({
    //         amountSats: 1000,
    //         receiverSparkAddress: targetAddress,
    //       }),
    //     ).rejects.toThrow(
    //       expect.objectContaining({
    //         name: ValidationError.name,
    //         message: expect.stringMatching(/Invalid Spark address prefix/),
    //         context: expect.objectContaining({
    //           field: "address",
    //           value: targetAddress,
    //         }),
    //       }),
    //     );
    //   },
    // );

    // it.concurrent.each(networkTypes)(
    //   "should fail transfer on same %s network due to no available leaves",
    //   async (network) => {
    //     const options: ConfigOptions = {
    //       network,
    //     };

    //     const { wallet: wallet1 } = await SparkWalletTesting.initialize({
    //       options,
    //     });

    //     const { wallet: wallet2 } = await SparkWalletTesting.initialize({
    //       options,
    //     });

    //     const address2 = await wallet2.getSparkAddress();

    //     await expect(
    //       wallet1.transfer({
    //         amountSats: 1000,
    //         receiverSparkAddress: address2,
    //       }),
    //     ).rejects.toThrow(
    //       expect.objectContaining({
    //         name: ValidationError.name,
    //         message: expect.stringMatching(/No owned leaves found/),
    //       }),
    //     );
    //   },
    // );
  });
});
