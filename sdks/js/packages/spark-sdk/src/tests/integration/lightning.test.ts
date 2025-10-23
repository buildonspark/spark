import { afterEach, beforeAll, describe, expect, it } from "@jest/globals";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { equalBytes } from "@scure/btc-signer/utils";
import { uuidv7 } from "uuidv7";
import { getTxFromRawTxBytes, KeyDerivationType } from "../../index.js";
import {
  BitcoinNetwork,
  CurrencyUnit,
  SparkProto,
  LightningReceiveRequest,
  LightningReceiveRequestStatus,
} from "../../types/index.js";
import { BitcoinFaucet, walletTypes } from "../test-utils.js";
import { SparkWalletTestingIntegration } from "../utils/spark-testing-wallet.js";

const { TransferStatus } = SparkProto;

const fakeInvoiceCreator = async (): Promise<LightningReceiveRequest> => {
  return {
    id: "123",
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
    network: BitcoinNetwork.REGTEST,

    status: LightningReceiveRequestStatus.INVOICE_CREATED,
    typename: "LightningReceiveRequest",
    invoice: {
      encodedInvoice:
        "lnbcrt1u1p5vxn7cpp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8essp5dlpmev9m3yxyak47ncnz9a0jyt2su2hulw4t97etewgkvrtjtl9sxq9z0rgqnp4qtlyk6hxw5h4hrdfdkd4nh2rv0mwyyqvdtakr3dv6m4vvsmfshvg6rzjqgp0s738klwqef7yr8yu54vv3wfuk4psv46x5laf6l6v5x4lwwahvqqqqrusum7gtyqqqqqqqqqqqqqq9qcqzpgdqq9qyyssq0evxvv962npnvsw8zxsghcty5j9du55yhkjm8qnlr760qdjvn0gsnr650wclqcvc90mpm6e493sy8ds4hxk2h0828nwlmdc64mtr87cqp9eq8w",
      bitcoinNetwork: BitcoinNetwork.REGTEST,
      paymentHash: bytesToHex(
        sha256(
          hexToBytes(
            "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
          ),
        ),
      ),
      amount: {
        originalValue: 100000,
        originalUnit: CurrencyUnit.MILLISATOSHI,
        preferredCurrencyUnit: CurrencyUnit.USD,
        preferredCurrencyValueRounded: 11,
        preferredCurrencyValueApprox: 11.45475372279496,
      },
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 1000 * 60 * 60 * 24).toISOString(),
    },
  };
};

describe.each(walletTypes)(
  "LightningService",
  ({ name, Signer, createTree }) => {
    let userWallet: SparkWalletTestingIntegration;
    let lightningService: ReturnType<
      SparkWalletTestingIntegration["getLightningService"]
    >;
    let transferService: ReturnType<
      SparkWalletTestingIntegration["getTransferService"]
    >;

    let sspWallet: SparkWalletTestingIntegration;
    let sspLightningService: ReturnType<
      SparkWalletTestingIntegration["getLightningService"]
    >;
    let sspTransferService: ReturnType<
      SparkWalletTestingIntegration["getTransferService"]
    >;
    let userIdentityPublicKey: Uint8Array;
    let sspIdentityPublicKey: Uint8Array;

    beforeAll(async () => {
      const { wallet: wallet1 } =
        await SparkWalletTestingIntegration.initialize({
          options: {
            network: "LOCAL",
          },
          signer: new Signer(),
        });
      userWallet = wallet1;
      userIdentityPublicKey = await userWallet
        .getSigner()
        .getIdentityPublicKey();
      lightningService = userWallet.getLightningService();
      transferService = userWallet.getTransferService();

      const { wallet: wallet2 } =
        await SparkWalletTestingIntegration.initialize({
          options: {
            network: "LOCAL",
          },
          signer: new Signer(),
        });
      sspWallet = wallet2;
      sspIdentityPublicKey = await sspWallet.getSigner().getIdentityPublicKey();
      sspLightningService = sspWallet.getLightningService();
      sspTransferService = sspWallet.getTransferService();
    });

    afterEach(async () => {
      // Clean up preimage shares
      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);
      const signingOperators = userWallet
        .getConfigService()
        .getSigningOperators();
      const connectionManager = userWallet.getConnectionManager();
      for (const operator of Object.values(signingOperators)) {
        const client = await connectionManager.createMockClient(
          operator.address,
        );
        await client.clean_up_preimage_share({ paymentHash });
        client.close();
      }
    });

    it(`${name} - should create an invoice`, async () => {
      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );

      const invoice = await lightningService.createLightningInvoiceWithPreImage(
        {
          invoiceCreator: fakeInvoiceCreator,
          amountSats: 100,
          memo: "test",
          preimage,
        },
      );

      expect(invoice).toBeDefined();
    });

    it(`${name} - test receive lightning payment`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);

      const invoice = await lightningService.createLightningInvoiceWithPreImage(
        {
          invoiceCreator: fakeInvoiceCreator,
          amountSats: 100,
          memo: "test",
          preimage,
        },
      );

      expect(invoice).toBeDefined();

      const leafId = uuidv7();
      const nodeToSend = await createTree(sspWallet, leafId, faucet, 12345n);
      const expiryTime = new Date(Date.now() + 2 * 60 * 1000);

      const newDerivationPath = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      } as const;

      const leaves = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          } as const,
          newKeyDerivation: newDerivationPath,
        },
      ];

      const response = await sspLightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: userIdentityPublicKey,
        paymentHash,
        isInboundPayment: true,
        expiryTime,
      });

      expect(equalBytes(response.preimage, preimage)).toBe(true);

      const senderTransfer = response.transfer;

      if (!senderTransfer) {
        throw new Error("test: Sender transfer not found");
      }

      const transfer = await sspTransferService.deliverTransferPackage(
        senderTransfer,
        leaves,
        new Map(),
        new Map(),
        new Map(),
      );

      expect(transfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
      );

      const pendingTransfer = await transferService.queryPendingTransfers();

      expect(pendingTransfer.transfers.length).toBe(1);

      const receiverTransfer = pendingTransfer.transfers[0];

      if (!receiverTransfer) {
        throw new Error("test: Receiver transfer not found");
      }

      expect(receiverTransfer.id).toEqual(senderTransfer.id);

      const leafPrivKeyMap =
        await transferService.verifyPendingTransfer(receiverTransfer);

      const leafPrivKeyMapBytes = leafPrivKeyMap.get(nodeToSend.id);
      if (!leafPrivKeyMapBytes) {
        throw new Error("test: Leaf private key not found");
      }
      expect(
        equalBytes(
          leafPrivKeyMapBytes,
          await sspWallet
            .getSigner()
            .getPublicKeyFromDerivation(newDerivationPath),
        ),
      ).toBe(true);

      const receiverLeaf = receiverTransfer.leaves[0];
      if (!receiverLeaf || !receiverLeaf.leaf) {
        throw new Error("test: Receiver leaf not found");
      }

      const claimingNodes = receiverTransfer.leaves.map((leaf) => {
        if (!leaf.leaf) {
          throw new Error("test: Leaf not found");
        }
        return {
          leaf: leaf.leaf,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher,
          } as const,
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf.id,
          } as const,
        };
      });

      await transferService.claimTransfer(receiverTransfer, claimingNodes);
    }, 60000);

    it(`${name} - test receive lightning v2 payment`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);

      const invoice = await lightningService.createLightningInvoiceWithPreImage(
        {
          invoiceCreator: fakeInvoiceCreator,
          amountSats: 100,
          memo: "test",
          preimage,
        },
      );

      expect(invoice).toBeDefined();

      const leafId = uuidv7();
      const nodeToSend = await createTree(sspWallet, leafId, faucet, 12345n);
      const expiryTime = new Date(Date.now() + 2 * 60 * 1000);

      const newKeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      } as const;
      const leaves = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          } as const,
          newKeyDerivation,
        },
      ];

      const response = await sspLightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: userIdentityPublicKey,
        paymentHash,
        isInboundPayment: true,
        expiryTime,
      });

      expect(equalBytes(response.preimage, preimage)).toBe(true);

      const senderTransfer = response.transfer;

      if (!senderTransfer) {
        throw new Error("test: Sender transfer not found");
      }

      const transfer = await sspTransferService.deliverTransferPackage(
        senderTransfer,
        leaves,
        new Map(),
        new Map(),
        new Map(),
      );

      expect(transfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
      );

      const pendingTransfer = await transferService.queryPendingTransfers();

      expect(pendingTransfer.transfers.length).toBe(1);

      const receiverTransfer = pendingTransfer.transfers[0];

      if (!receiverTransfer) {
        throw new Error("test: Receiver transfer not found");
      }

      expect(receiverTransfer.id).toEqual(senderTransfer.id);

      const leafPrivKeyMap =
        await transferService.verifyPendingTransfer(receiverTransfer);

      const leafPrivKeyMapBytes = leafPrivKeyMap.get(nodeToSend.id);
      if (!leafPrivKeyMapBytes) {
        throw new Error("test: Leaf private key not found");
      }
      expect(
        equalBytes(
          leafPrivKeyMapBytes,
          await sspWallet
            .getSigner()
            .getPublicKeyFromDerivation(newKeyDerivation),
        ),
      ).toBe(true);

      const receiverLeaf = receiverTransfer.leaves[0];
      if (!receiverLeaf || !receiverLeaf.leaf) {
        throw new Error("test: Receiver leaf not found");
      }

      const claimingNodes = receiverTransfer.leaves.map((leaf) => {
        if (!leaf.leaf) {
          throw new Error("test: Leaf not found");
        }
        return {
          leaf: leaf.leaf,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher,
          } as const,
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf.id,
          } as const,
        };
      });

      await transferService.claimTransfer(receiverTransfer, claimingNodes);
    }, 60000);

    it(`${name} - test send lightning payment`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);

      const leafId = uuidv7();
      const expiryTime = new Date(Date.now() + 2 * 60 * 1000);
      const nodeToSend = await createTree(userWallet, leafId, faucet, 12345n);

      const newKeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      } as const;

      const leaves = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          } as const,
          newKeyDerivation,
        },
      ];

      const response = await lightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: sspIdentityPublicKey,
        paymentHash,
        isInboundPayment: false,
        invoiceString: (await fakeInvoiceCreator()).invoice.encodedInvoice,
        expiryTime,
      });

      if (!response.transfer) {
        throw new Error("test: Transfer not found");
      }

      const transfer = await transferService.deliverTransferPackage(
        response.transfer,
        leaves,
        new Map(),
        new Map(),
        new Map(),
      );

      expect(transfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
      );

      const refunds =
        await sspLightningService.queryUserSignedRefunds(paymentHash);

      let expectedValue = 0n;
      for (const leaf of transfer.leaves) {
        const cpfpRefund = getTxFromRawTxBytes(leaf.intermediateRefundTx);
        expectedValue += cpfpRefund.getOutput(0)?.amount || 0n;
      }

      let totalValue = 0n;
      for (const refund of refunds) {
        const value = sspLightningService.validateUserSignedRefund(refund);
        totalValue += value;
      }

      expect(totalValue).toBe(expectedValue);
      const receiverTransfer =
        await sspLightningService.providePreimage(preimage);

      expect(receiverTransfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
      );
      expect(receiverTransfer.id).toEqual(transfer.id);

      const leafPrivKeyMap =
        await sspTransferService.verifyPendingTransfer(receiverTransfer);

      const leafPrivKeyMapBytes = leafPrivKeyMap.get(nodeToSend.id);
      if (!leafPrivKeyMapBytes) {
        throw new Error("test: Leaf private key not found");
      }
      expect(
        equalBytes(
          leafPrivKeyMapBytes,
          await userWallet
            .getSigner()
            .getPublicKeyFromDerivation(newKeyDerivation),
        ),
      ).toBe(true);

      const receiverLeaf = receiverTransfer.leaves[0];
      if (!receiverLeaf || !receiverLeaf.leaf) {
        throw new Error("test: Receiver leaf not found");
      }

      const claimingNodes = receiverTransfer.leaves.map((leaf) => {
        if (!leaf.leaf) {
          throw new Error("test: Leaf not found");
        }
        return {
          leaf: leaf.leaf,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher,
          } as const,
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf.id,
          } as const,
        };
      });

      await sspTransferService.claimTransfer(receiverTransfer, claimingNodes);
    }, 60000);

    it(`${name} - test send lightning v2 payment`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);

      const leafId = uuidv7();
      const transferID = uuidv7();
      const expiryTime = new Date(Date.now() + 2 * 60 * 1000);
      const nodeToSend = await createTree(userWallet, leafId, faucet, 12345n);

      const newKeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      } as const;

      const leaves = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          } as const,
          newKeyDerivation,
        },
      ];

      const startTransferRequest =
        await transferService.prepareTransferForLightning(
          leaves,
          sspIdentityPublicKey,
          paymentHash,
          expiryTime,
          transferID,
        );

      const response = await lightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: sspIdentityPublicKey,
        paymentHash,
        isInboundPayment: false,
        invoiceString: (await fakeInvoiceCreator()).invoice.encodedInvoice,
        startTransferRequest,
        expiryTime,
        transferID,
      });

      if (!response.transfer) {
        throw new Error("test: Transfer not found");
      }

      const transfer = response.transfer;

      expect(transfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
      );

      const refunds =
        await sspLightningService.queryUserSignedRefunds(paymentHash);

      let expectedValue = 0n;
      for (const leaf of transfer.leaves) {
        const cpfpRefund = getTxFromRawTxBytes(leaf.intermediateRefundTx);
        expectedValue += cpfpRefund.getOutput(0)?.amount || 0n;
      }

      let totalValue = 0n;
      for (const refund of refunds) {
        const value = sspLightningService.validateUserSignedRefund(refund);
        totalValue += value;
      }

      expect(totalValue).toBe(expectedValue);
      const receiverTransfer =
        await sspLightningService.providePreimage(preimage);

      expect(receiverTransfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
      );
      expect(receiverTransfer.id).toEqual(transfer.id);

      const leafPrivKeyMap =
        await sspTransferService.verifyPendingTransfer(receiverTransfer);

      const leafPrivKeyMapBytes = leafPrivKeyMap.get(nodeToSend.id);
      if (!leafPrivKeyMapBytes) {
        throw new Error("test: Leaf private key not found");
      }
      expect(
        equalBytes(
          leafPrivKeyMapBytes,
          await userWallet
            .getSigner()
            .getPublicKeyFromDerivation(newKeyDerivation),
        ),
      ).toBe(true);

      const receiverLeaf = receiverTransfer.leaves[0];
      if (!receiverLeaf || !receiverLeaf.leaf) {
        throw new Error("test: Receiver leaf not found");
      }

      const claimingNodes = receiverTransfer.leaves.map((leaf) => {
        if (!leaf.leaf) {
          throw new Error("test: Leaf not found");
        }
        return {
          leaf: leaf.leaf,
          keyDerivation: {
            type: KeyDerivationType.ECIES,
            path: leaf.secretCipher,
          } as const,
          newKeyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leaf.leaf.id,
          } as const,
        };
      });

      await sspTransferService.claimTransfer(receiverTransfer, claimingNodes);
    }, 60000);
  },
);
