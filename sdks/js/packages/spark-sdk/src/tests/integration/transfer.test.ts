import { describe, expect, it, jest } from "@jest/globals";
import { bytesToHex, equalBytes, hexToBytes } from "@noble/curves/utils";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { uuidv7 } from "uuidv7";
import { RPCError } from "../../errors/types.js";
import { KeyDerivation, KeyDerivationType } from "../../index.js";
import { TransferStatus } from "../../proto/spark.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { SigningService } from "../../services/signing.js";
import type { LeafKeyTweak } from "../../services/transfer.js";
import { TransferService } from "../../services/transfer.js";
import {
  ConfigOptions,
  getLocalSigningOperators,
  WalletConfig,
} from "../../services/wallet-config.js";
import { NetworkType } from "../../utils/network.js";
import { walletTypes } from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

const testLocalOnly = process.env.GITHUB_ACTIONS ? it.skip : it;

describe.each(walletTypes)(
  "Transfer with name",
  ({ name, Signer, createTree }) => {
    jest.setTimeout(15_000);
    it(`${name} - test transfer`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const options: ConfigOptions = {
        network: "LOCAL",
      };

      const { wallet: senderWallet } = await SparkWalletTesting.initialize({
        options,
        signer: new Signer(),
      });

      const senderConfigService = new WalletConfigService(
        options,
        senderWallet.getSigner(),
      );
      const senderConnectionManager = new ConnectionManager(
        senderConfigService,
      );
      const signingService = new SigningService(senderConfigService);
      const senderTransferService = new TransferService(
        senderConfigService,
        senderConnectionManager,
        signingService,
      );

      const leafId = uuidv7();
      const rootNode = await createTree(senderWallet, leafId, faucet, 1000n);

      const newLeafDerivationPath: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };

      const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
        options,
        signer: new Signer(),
      });
      const receiverPubkey = await receiverWallet.getIdentityPublicKey();

      const receiverConfigService = new WalletConfigService(
        options,
        receiverWallet.getSigner(),
      );
      const receiverConnectionManager = new ConnectionManager(
        receiverConfigService,
      );
      const receiverSigningService = new SigningService(receiverConfigService);

      const receiverTransferService = new TransferService(
        receiverConfigService,
        receiverConnectionManager,
        receiverSigningService,
      );

      const transferNode: LeafKeyTweak = {
        leaf: rootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leafId,
        },
        newKeyDerivation: newLeafDerivationPath,
      };

      const senderTransfer =
        await senderTransferService.sendTransferWithKeyTweaks(
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
    }, 30000);

    testLocalOnly(`${name} - test transfer with separate`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const options: ConfigOptions = {
        network: "LOCAL",
      };
      const { wallet: senderWallet } = await SparkWalletTesting.initialize({
        options,
        signer: new Signer(),
      });

      const senderConfigService = new WalletConfigService(
        options,
        senderWallet.getSigner(),
      );
      const senderConnectionManager = new ConnectionManager(
        senderConfigService,
      );
      const senderSigningService = new SigningService(senderConfigService);

      const senderTransferService = new TransferService(
        senderConfigService,
        senderConnectionManager,
        senderSigningService,
      );

      const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
        options,
        signer: new Signer(),
      });
      const receiverPubkey = await receiverWallet.getIdentityPublicKey();

      const receiverConfigService = new WalletConfigService(
        options,
        receiverWallet.getSigner(),
      );
      const receiverConnectionManager = new ConnectionManager(
        receiverConfigService,
      );
      const receiverSigningService = new SigningService(receiverConfigService);

      const receiverTransferService = new TransferService(
        receiverConfigService,
        receiverConnectionManager,
        receiverSigningService,
      );

      const leafId = uuidv7();
      const rootNode = await createTree(senderWallet, leafId, faucet, 100_000n);

      const newLeafDerivationPath: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };

      const transferNode: LeafKeyTweak = {
        leaf: rootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leafId,
        },
        newKeyDerivation: newLeafDerivationPath,
      };

      const leavesToTransfer = [transferNode];

      const senderTransfer =
        await senderTransferService.sendTransferWithKeyTweaks(
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
      expect(
        equalBytes(
          leafPrivKeyMapBytes!,
          await senderWallet
            .getSigner()
            .getPublicKeyFromDerivation(newLeafDerivationPath),
        ),
      ).toBe(true);

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

      const transferService = new TransferService(
        receiverConfigService,
        new ConnectionManager(receiverConfigService),
        new SigningService(receiverConfigService),
      );

      await transferService.claimTransferTweakKeys(
        receiverTransfer!,
        claimingNodes,
      );

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
      expect(bytesToHex(newLeafPubKeyMapBytes!)).toBe(
        bytesToHex(
          await senderWallet
            .getSigner()
            .getPublicKeyFromDerivation(newLeafDerivationPath),
        ),
      );

      await transferService.claimTransferSignRefunds(
        newReceiverTransfer!,
        claimingNodes,
      );

      const newNewPendingTransfer =
        await receiverWallet.queryPendingTransfers();
      expect(newNewPendingTransfer.transfers.length).toBe(1);

      await receiverTransferService.claimTransfer(
        newNewPendingTransfer.transfers[0]!,
        claimingNodes,
      );
    });

    testLocalOnly(`${name} - cancel transfer`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const options: ConfigOptions = {
        network: "LOCAL",
      };
      const { wallet: senderWallet } = await SparkWalletTesting.initialize({
        options,
        signer: new Signer(),
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
      const receiverSigningService = new SigningService(receiverConfigService);

      const receiverTransferService = new TransferService(
        receiverConfigService,
        receiverConnectionManager,
        receiverSigningService,
      );

      const leafId = uuidv7();
      const rootNode = await createTree(senderWallet, leafId, faucet, 100_000n);

      const newLeafDerivationPath: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };

      const transferNode: LeafKeyTweak = {
        leaf: rootNode,
        keyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leafId,
        },
        newKeyDerivation: newLeafDerivationPath,
      };

      const senderConfigService = new WalletConfigService(
        options,
        senderWallet.getSigner(),
      );
      const senderConnectionManager = new ConnectionManager(
        senderConfigService,
      );
      const senderSigningService = new SigningService(senderConfigService);

      const senderTransferService = new TransferService(
        senderConfigService,
        senderConnectionManager,
        senderSigningService,
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

      const newSenderTransfer =
        await senderTransferService.sendTransferWithKeyTweaks(
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
      expect(
        equalBytes(
          leafPubKeyMapBytes!,
          await senderWallet
            .getSigner()
            .getPublicKeyFromDerivation(newLeafDerivationPath),
        ),
      ).toBe(true);

      const claimingNodes: LeafKeyTweak[] = receiverTransfer!.leaves.map(
        (leaf) => ({
          leaf: receiverTransfer!.leaves[0]!.leaf!,
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
    });

    testLocalOnly(
      `${name} - test that when the receiver has tweaked the key on some SOs, we can still claim the transfer`,
      async () => {
        const faucet = BitcoinFaucet.getInstance();

        const options: ConfigOptions = {
          network: "LOCAL",
        };

        const { wallet: senderWallet } = await SparkWalletTesting.initialize({
          options,
          signer: new Signer(),
        });

        const senderConfigService = new WalletConfigService(
          options,
          senderWallet.getSigner(),
        );
        const senderConnectionManager = new ConnectionManager(
          senderConfigService,
        );
        const senderSigningService = new SigningService(senderConfigService);
        const senderTransferService = new TransferService(
          senderConfigService,
          senderConnectionManager,
          senderSigningService,
        );

        const leafId = uuidv7();
        const rootNode = await createTree(senderWallet, leafId, faucet, 1000n);

        const newLeafDerivationPath: KeyDerivation = {
          type: KeyDerivationType.LEAF,
          path: uuidv7(),
        };

        const soToRemove =
          "0000000000000000000000000000000000000000000000000000000000000005";
        const localSigningOperators = getLocalSigningOperators();
        const signingOperators = Object.fromEntries(
          Object.entries(localSigningOperators).filter(
            ([key]) => key !== soToRemove,
          ),
        );
        const missingOperatorOptions = {
          ...WalletConfig.LOCAL,
          signingOperators,
        };
        const mnemonic = generateMnemonic(wordlist);
        const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
          options: missingOperatorOptions,
          mnemonicOrSeed: mnemonic,
          signer: new Signer(),
        });

        const receiverPubkey = await receiverWallet.getIdentityPublicKey();

        const receiverConfigService = new WalletConfigService(
          missingOperatorOptions,
          receiverWallet.getSigner(),
        );
        const receiverConnectionManager = new ConnectionManager(
          receiverConfigService,
        );
        const receiverSigningService = new SigningService(
          receiverConfigService,
        );
        const receiverTransferService = new TransferService(
          receiverConfigService,
          receiverConnectionManager,
          receiverSigningService,
        );

        const transferNode: LeafKeyTweak = {
          leaf: rootNode,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          },
          newKeyDerivation: newLeafDerivationPath,
        };

        const senderTransfer =
          await senderTransferService.sendTransferWithKeyTweaks(
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
        expect(bytesToHex(leafPrivKeyMapBytes!)).toBe(
          bytesToHex(
            await senderWallet
              .getSigner()
              .getPublicKeyFromDerivation(newLeafDerivationPath),
          ),
        );

        const claimingNodes: LeafKeyTweak[] = receiverTransfer!.leaves.map(
          (leaf) => ({
            leaf: rootNode,
            keyDerivation: {
              type: KeyDerivationType.ECIES,
              path: receiverTransfer!.leaves[0]!.secretCipher,
            },
            newKeyDerivation: {
              type: KeyDerivationType.LEAF,
              path: leaf.leaf!.id,
            },
          }),
        );

        // Tweak the key with only 4 out of the 5 operators
        await receiverTransferService.claimTransferTweakKeys(
          receiverTransfer!,
          claimingNodes,
        );

        const receiverOptions = {
          ...WalletConfig.LOCAL,
        };

        const { wallet: receiverWalletWithAllOperators } =
          await SparkWalletTesting.initialize({
            options: receiverOptions,
            mnemonicOrSeed: mnemonic,
            signer: new Signer(),
          });
        const receiverConfigServiceWithAllOperators = new WalletConfigService(
          receiverOptions,
          receiverWalletWithAllOperators.getSigner(),
        );
        const receiverConnectionManagerWithAllOperators = new ConnectionManager(
          receiverConfigServiceWithAllOperators,
        );
        const receiverSigningServiceWithAllOperators = new SigningService(
          receiverConfigServiceWithAllOperators,
        );

        const receiverTransferServiceWithAllOperators = new TransferService(
          receiverConfigServiceWithAllOperators,
          receiverConnectionManagerWithAllOperators,
          receiverSigningServiceWithAllOperators,
        );

        const { wallet: receiverWalletWithMissingOperatorAsCoordinator } =
          await SparkWalletTesting.initialize({
            options: {
              ...WalletConfig.LOCAL,
              coordinatorIdentifier: soToRemove,
            },
            mnemonicOrSeed: mnemonic,
            signer: new Signer(),
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
          bytesToHex(
            await senderWallet
              .getSigner()
              .getPublicKeyFromDerivation(newLeafDerivationPath),
          ),
        );

        await receiverWalletWithAllOperators.verifyPendingTransfer(
          receiverTransfer!,
        );

        await receiverTransferServiceWithAllOperators.claimTransfer(
          receiverTransfer!,
          claimingNodes,
        );
      },
    );

    it(`${name} - test incoming transfer rpc stream`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const options: ConfigOptions = {
        network: "LOCAL",
      };

      const { wallet: senderWallet } = await SparkWalletTesting.initialize({
        options,
        signer: new Signer(),
      });

      const depositAddress = await senderWallet.getSingleUseDepositAddress();

      const signedTx = await faucet.sendToAddress(depositAddress, 1_000n);

      await senderWallet.claimDeposit(signedTx.id);

      const { wallet: receiverWallet } = await SparkWalletTesting.initialize(
        {
          options,
        },
        false,
      );

      expect(await receiverWallet.getSparkAddress()).not.toEqual(
        await senderWallet.getSparkAddress(),
      );

      const transfer = await senderWallet.transfer({
        amountSats: 1000,
        receiverSparkAddress: await receiverWallet.getSparkAddress(),
      });

      async function waitForTransferClaim(
        transferId: string,
        timeoutMs: number,
      ): Promise<{ transferId: string; balance: bigint }> {
        return new Promise((resolve, reject) => {
          const timeout = setTimeout(() => {
            receiverWallet.removeListener("transfer:claimed", handler);
            reject(
              new Error(
                `Timeout waiting for transfer ${transferId} to be claimed`,
              ),
            );
          }, timeoutMs);

          const handler = (claimedTransferId: string, balance: bigint) => {
            if (claimedTransferId === transferId) {
              clearTimeout(timeout);
              receiverWallet.removeListener("transfer:claimed", handler);
              resolve({ transferId: claimedTransferId, balance });
            }
          };

          receiverWallet.on("transfer:claimed", handler);
        });
      }

      const result = await waitForTransferClaim(transfer.id, 10000);
      expect(result.transferId).toBe(transfer.id);
      expect(result.balance).toBe(1000n);
      const receiverBalance = await receiverWallet.getBalance();
      expect(receiverBalance.balance).toBe(1000n);
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

    describe.skip("address validation", () => {
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
      //       network: sourceNetwork
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
  },
);

describe.each(walletTypes)("transfer v2", ({ name, Signer, createTree }) => {
  jest.setTimeout(15_000);
  it(`${name} - test transfer with pretweaked package`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const options: ConfigOptions = {
      network: "LOCAL",
    };

    const { wallet: senderWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });

    const senderConfigService = new WalletConfigService(
      options,
      senderWallet.getSigner(),
    );
    const senderConnectionManager = new ConnectionManager(senderConfigService);
    const signingService = new SigningService(senderConfigService);
    const senderTransferService = new TransferService(
      senderConfigService,
      senderConnectionManager,
      signingService,
    );

    const leafId = uuidv7();
    const rootNode = await createTree(senderWallet, leafId, faucet, 1000n);

    const newLeafDerivationPath: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: uuidv7(),
    };

    const { wallet: receiverWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });
    const receiverPubkey = await receiverWallet.getIdentityPublicKey();

    const receiverConfigService = new WalletConfigService(
      options,
      receiverWallet.getSigner(),
    );
    const receiverConnectionManager = new ConnectionManager(
      receiverConfigService,
    );
    const receiverSigningService = new SigningService(receiverConfigService);

    const receiverTransferService = new TransferService(
      receiverConfigService,
      receiverConnectionManager,
      receiverSigningService,
    );

    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leafId,
      },
      newKeyDerivation: newLeafDerivationPath,
    };

    const senderTransfer =
      await senderTransferService.sendTransferWithKeyTweaks(
        [transferNode],
        hexToBytes(receiverPubkey),
      );

    const pendingTransfer = await receiverWallet.queryPendingTransfers();

    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];

    expect(receiverTransfer!.id).toBe(senderTransfer.id);
    expect(receiverTransfer!.expiryTime?.getTime() ?? 0).toBeLessThan(
      Date.now(),
    );

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
  }, 30000);

  it(`${name} - test self transfer with pretweaked package`, async () => {
    const faucet = BitcoinFaucet.getInstance();
    const options: ConfigOptions = {
      network: "LOCAL",
    };
    const { wallet: senderWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });
    const senderConfigService = new WalletConfigService(
      options,
      senderWallet.getSigner(),
    );
    const senderConnectionManager = new ConnectionManager(senderConfigService);
    const senderSigningService = new SigningService(senderConfigService);
    const senderTransferService = new TransferService(
      senderConfigService,
      senderConnectionManager,
      senderSigningService,
    );
    const leafId = uuidv7();
    const rootNode = await createTree(senderWallet, leafId, faucet, 1000n);
    const newLeafDerivationPath: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: uuidv7(),
    };
    const receiverPubkey = await senderWallet.getIdentityPublicKey();
    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leafId,
      },
      newKeyDerivation: newLeafDerivationPath,
    };
    const senderTransfer =
      await senderTransferService.sendTransferWithKeyTweaks(
        [transferNode],
        hexToBytes(receiverPubkey),
      );
    const receiverTransfer = await senderTransferService.queryTransfer(
      senderTransfer.id,
    );
    expect(receiverTransfer!.id).toBe(senderTransfer.id);
    expect(receiverTransfer!.expiryTime?.getTime() ?? 0).toBeLessThan(
      Date.now(),
    );

    const claimingNodes: LeafKeyTweak[] = receiverTransfer!.leaves.map(
      (leaf) => ({
        leaf: rootNode,
        keyDerivation: {
          type: KeyDerivationType.ECIES,
          path: receiverTransfer!.leaves[0]!.secretCipher,
        },
        newKeyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.leaf!.id,
        },
      }),
    );
    await senderTransferService.claimTransfer(receiverTransfer!, claimingNodes);

    const balance = await senderWallet.getBalance();
    expect(balance.balance).toBe(1000n);
  }, 30000);

  it(`${name} - test transfer with wallet`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    const depositResp = await sdk.getSingleUseDepositAddress();
    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);

    await sdk.claimDeposit(signedTx.id);

    const balance = await sdk.getBalance();
    expect(balance.balance).toBe(1_000n);

    const sparkAddress = await sdk.getSparkAddress();

    await sdk.transfer({
      amountSats: 1000,
      receiverSparkAddress: sparkAddress,
    });

    const newPendingTransfer = await sdk.queryPendingTransfers();
    expect(newPendingTransfer.transfers.length).toBe(0);
    const newBalance = await sdk.getBalance();
    expect(newBalance.balance).toBe(1000n);
  });

  it(`${name} - test transfer with retry`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    const depositResp = await sdk.getSingleUseDepositAddress();
    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);

    await sdk.claimDeposit(signedTx.id);

    const balance = await sdk.getBalance();
    expect(balance.balance).toBe(1_000n);

    const { wallet: sdk2 } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    await sdk.transfer({
      amountSats: 1000,
      receiverSparkAddress: await sdk2.getSparkAddress(),
    });

    const pendingTransfers = await sdk2.queryPendingTransfers();
    expect(pendingTransfers.transfers.length).toBe(1);
    const transfer = pendingTransfers.transfers[0]!;

    const originalClaimTransferCore = (sdk2 as any).claimTransferCore.bind(
      sdk2,
    );
    const claimTransferCoreSpy = jest
      .spyOn(sdk2 as any, "claimTransferCore")
      .mockRejectedValueOnce(new Error("Network error"))
      .mockImplementation(async (transfer) => {
        return await originalClaimTransferCore(transfer);
      });

    await (sdk2 as any).claimTransfer({ transfer });

    expect(claimTransferCoreSpy).toHaveBeenCalledTimes(2);
    expect((await sdk2.getBalance()).balance).toBe(1000n);
  });

  it(`${name} - test claiming already claimed transfer`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    const depositResp = await sdk.getSingleUseDepositAddress();

    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);

    await sdk.claimDeposit(signedTx.id);

    const balance = await sdk.getBalance();
    expect(balance.balance).toBe(1_000n);

    const { wallet: sdk2 } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    await sdk.transfer({
      amountSats: 1000,
      receiverSparkAddress: await sdk2.getSparkAddress(),
    });

    const pendingTransfers = await sdk2.queryPendingTransfers();
    expect(pendingTransfers.transfers.length).toBe(1);
    const transfer = pendingTransfers.transfers[0]!;

    await (sdk2 as any).claimTransfer({ transfer });

    const claimTransferCoreSpy = jest.spyOn(sdk2 as any, "claimTransferCore");

    const claim1 = await (sdk2 as any).claimTransfer({
      transfer: {
        ...transfer,
        status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
      },
    });
    expect(claim1.length).toBe(0);

    const claim2 = await (sdk2 as any).claimTransfer({
      transfer: {
        ...transfer,
        status: TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
      },
    });
    expect(claim2.length).toBe(0);

    const claim3 = await (sdk2 as any).claimTransfer({
      transfer,
    });

    expect(claim3.length).toBe(0);

    // Expect 3 because we call claimTransfer 3 times and we expect there to be 0 retries
    expect(claimTransferCoreSpy).toHaveBeenCalledTimes(3);
  });

  it(`${name} - test querying updated transfer after error`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const options: ConfigOptions = {
      network: "LOCAL",
    };

    const { wallet: sdk } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });

    const depositResp = await sdk.getSingleUseDepositAddress();

    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);

    await sdk.claimDeposit(signedTx.id);

    const balance = await sdk.getBalance();
    expect(balance.balance).toBe(1_000n);

    const { wallet: sdk2 } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    const receiverConfigService = new WalletConfigService(
      options,
      sdk2.getSigner(),
    );
    const receiverConnectionManager = new ConnectionManager(
      receiverConfigService,
    );
    const receiverSigningService = new SigningService(receiverConfigService);
    const receiverTransferService = new TransferService(
      receiverConfigService,
      receiverConnectionManager,
      receiverSigningService,
    );

    await sdk.transfer({
      amountSats: 1000,
      receiverSparkAddress: await sdk2.getSparkAddress(),
    });

    const pendingTransfers = await sdk2.queryPendingTransfers();
    expect(pendingTransfers.transfers.length).toBe(1);
    const transfer = pendingTransfers.transfers[0]!;

    const leaves: LeafKeyTweak[] = transfer.leaves.map((leaf) => ({
      leaf: {
        ...leaf.leaf!,
        refundTx: leaf.intermediateRefundTx,
        directRefundTx: leaf.intermediateDirectRefundTx,
        directFromCpfpRefundTx: leaf.intermediateDirectFromCpfpRefundTx,
      },
      keyDerivation: {
        type: KeyDerivationType.ECIES,
        path: leaf.secretCipher,
      },
      newKeyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leaf.leaf!.id,
      },
    }));

    await receiverTransferService.claimTransferTweakKeys(transfer, leaves);

    const claimTransferCoreSpy = jest.spyOn(sdk2 as any, "claimTransferCore");

    const res = await (sdk2 as any).claimTransfer({ transfer });
    expect(res.length).toBe(1);

    expect(claimTransferCoreSpy).toHaveBeenCalledTimes(2);
  });

  it(`${name} - transfer between two wallets that are using different coordinators`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const localOperators = Object.values(getLocalSigningOperators());
    const { wallet: alice } = await SparkWalletTesting.initialize({
      options: {
        network: "LOCAL",
        coordinatorIdentifier: localOperators[0]!.identifier,
      },
      signer: new Signer(),
    });
    const depositResp = await alice.getSingleUseDepositAddress();

    if (!depositResp) {
      throw new RPCError("Deposit address not found", {
        method: "getDepositAddress",
      });
    }

    const signedTx = await faucet.sendToAddress(depositResp, 1_000n);

    await faucet.mineBlocks(1);

    await alice.claimDeposit(signedTx.id);

    const balance = await alice.getBalance();
    expect(balance.balance).toBe(1_000n);

    const options: ConfigOptions = {
      network: "LOCAL",
      coordinatorIdentifier: localOperators[1]!.identifier,
    };
    const { wallet: bob } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });

    const bobConfigService = new WalletConfigService(options, bob.getSigner());
    const bobConnectionManager = new ConnectionManager(bobConfigService);
    const bobSigningService = new SigningService(bobConfigService);

    const bobTransferService = new TransferService(
      bobConfigService,
      bobConnectionManager,
      bobSigningService,
    );

    const sparkAddress = await bob.getSparkAddress();

    await alice.transfer({
      amountSats: 1000,
      receiverSparkAddress: sparkAddress,
    });

    const pendingTransfers = await bob.queryPendingTransfers();
    expect(pendingTransfers.transfers.length).toBe(1);
    const transfer = pendingTransfers.transfers[0]!;

    const claimingNodes: LeafKeyTweak[] = transfer!.leaves.map((leaf) => ({
      leaf: leaf.leaf!,
      keyDerivation: {
        type: KeyDerivationType.ECIES,
        path: leaf.secretCipher,
      },
      newKeyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leaf.leaf!.id,
      },
    }));

    await bobTransferService.claimTransfer(transfer!, claimingNodes);
  });
});
