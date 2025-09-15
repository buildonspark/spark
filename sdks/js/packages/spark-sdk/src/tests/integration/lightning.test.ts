import { afterEach, beforeAll, describe, expect, it } from "@jest/globals";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { equalBytes } from "@scure/btc-signer/utils";
import { uuidv7 } from "uuidv7";
import LightningReceiveRequest from "../../graphql/objects/LightningReceiveRequest.js";
import {
  getTxFromRawTxBytes,
  KeyDerivation,
  KeyDerivationType,
} from "../../index.js";
import { TransferStatus } from "../../proto/spark.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { LightningService } from "../../services/lightning.js";
import { SigningService } from "../../services/signing.js";
import type { LeafKeyTweak } from "../../services/transfer.js";
import { TransferService } from "../../services/transfer.js";
import {
  BitcoinNetwork,
  CurrencyUnit,
  LightningReceiveRequestStatus,
} from "../../types/index.js";
import { getTestWalletConfig, walletTypes } from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

async function cleanUp() {
  const config = getTestWalletConfig();

  const preimage = hexToBytes(
    "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
  );
  const paymentHash = sha256(preimage);

  const configService = new WalletConfigService(config);
  const connectionManager = new ConnectionManager(configService);
  for (const operator of Object.values(config.signingOperators!)) {
    const client = await connectionManager.createMockClient(operator!.address);
    await client.clean_up_preimage_share({
      paymentHash,
    });
    client.close();
  }
}

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
    let userWallet: SparkWalletTesting;
    let userConfig: WalletConfigService;
    let lightningService: LightningService;
    let transferService: TransferService;
    let signingService: SigningService;

    let sspWallet: SparkWalletTesting;
    let sspConfig: WalletConfigService;
    let sspLightningService: LightningService;
    let sspTransferService: TransferService;
    let sspSigningService: SigningService;

    beforeAll(async () => {
      const { wallet: wallet1 } = await SparkWalletTesting.initialize({
        options: {
          network: "LOCAL",
        },
        signer: new Signer(),
      });

      userWallet = wallet1;

      userConfig = new WalletConfigService(
        {
          network: "LOCAL",
        },
        userWallet.getSigner(),
      );
      const connectionManager = new ConnectionManager(userConfig);
      signingService = new SigningService(userConfig);
      lightningService = new LightningService(
        userConfig,
        connectionManager,
        signingService,
      );
      transferService = new TransferService(
        userConfig,
        connectionManager,
        signingService,
      );

      const { wallet: wallet2 } = await SparkWalletTesting.initialize({
        options: {
          network: "LOCAL",
        },
        signer: new Signer(),
      });

      sspWallet = wallet2;

      sspConfig = new WalletConfigService(
        {
          network: "LOCAL",
        },
        sspWallet.getSigner(),
      );
      const sspConnectionManager = new ConnectionManager(sspConfig);
      sspSigningService = new SigningService(sspConfig);
      sspLightningService = new LightningService(
        sspConfig,
        sspConnectionManager,
        sspSigningService,
      );
      sspTransferService = new TransferService(
        sspConfig,
        sspConnectionManager,
        sspSigningService,
      );
    });
    afterEach(async () => {
      await cleanUp();
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

      const newDerivationPath: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };

      const leaves: LeafKeyTweak[] = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          },
          newKeyDerivation: newDerivationPath,
        },
      ];

      const response = await sspLightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: await userConfig.signer.getIdentityPublicKey(),
        paymentHash,
        isInboundPayment: true,
      });

      expect(equalBytes(response.preimage, preimage)).toBe(true);

      const senderTransfer = response.transfer;

      expect(senderTransfer).toBeDefined();

      const transfer = await sspTransferService.deliverTransferPackage(
        senderTransfer!,
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

      expect(receiverTransfer!.id).toEqual(senderTransfer!.id);

      const leafPrivKeyMap = await transferService.verifyPendingTransfer(
        receiverTransfer!,
      );

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.has(nodeToSend.id)).toBe(true);
      expect(
        equalBytes(
          leafPrivKeyMap.get(nodeToSend.id)!,
          await sspConfig.signer.getPublicKeyFromDerivation(newDerivationPath),
        ),
      ).toBe(true);

      const leaf = receiverTransfer!.leaves[0]!.leaf;
      expect(leaf).toBeDefined();

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

      await transferService.claimTransfer(receiverTransfer!, claimingNodes);
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

      const newKeyDerivation: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };
      const leaves: LeafKeyTweak[] = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          },
          newKeyDerivation,
        },
      ];

      const response = await sspLightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: await userConfig.signer.getIdentityPublicKey(),
        paymentHash,
        isInboundPayment: true,
      });

      expect(equalBytes(response.preimage, preimage)).toBe(true);

      const senderTransfer = response.transfer;

      expect(senderTransfer).toBeDefined();

      const transfer = await sspTransferService.deliverTransferPackage(
        senderTransfer!,
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

      expect(receiverTransfer!.id).toEqual(senderTransfer!.id);

      const leafPrivKeyMap = await transferService.verifyPendingTransfer(
        receiverTransfer!,
      );

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.has(nodeToSend.id)).toBe(true);
      expect(
        equalBytes(
          leafPrivKeyMap.get(nodeToSend.id)!,
          await sspConfig.signer.getPublicKeyFromDerivation(newKeyDerivation),
        ),
      ).toBe(true);

      const leaf = receiverTransfer!.leaves[0]!.leaf;
      expect(leaf).toBeDefined();

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

      await transferService.claimTransfer(receiverTransfer!, claimingNodes);
    }, 60000);

    it(`${name} - test send lightning payment`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);

      const leafId = uuidv7();
      const nodeToSend = await createTree(userWallet, leafId, faucet, 12345n);

      const newKeyDerivation: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };

      const leaves: LeafKeyTweak[] = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          },
          newKeyDerivation,
        },
      ];

      const response = await lightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: await sspConfig.signer.getIdentityPublicKey(),
        paymentHash,
        isInboundPayment: false,
        invoiceString: (await fakeInvoiceCreator()).invoice.encodedInvoice,
      });

      expect(response.transfer).toBeDefined();

      const transfer = await transferService.deliverTransferPackage(
        response.transfer!,
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
        const cpfpRefund = getTxFromRawTxBytes(leaf.leaf?.refundTx!);
        expectedValue += cpfpRefund.getOutput(0)?.amount || 0n;

        if ((leaf.leaf?.directRefundTx.length || 0) > 0) {
          const directRefund = getTxFromRawTxBytes(leaf.leaf?.directRefundTx!);
          expectedValue += directRefund.getOutput(0)?.amount || 0n;
        }

        if ((leaf.leaf?.directFromCpfpRefundTx.length || 0) > 0) {
          const directFromCpfpRefund = getTxFromRawTxBytes(
            leaf.leaf?.directFromCpfpRefundTx!,
          );
          expectedValue += directFromCpfpRefund.getOutput(0)?.amount || 0n;
        }
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

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.has(nodeToSend.id)).toBe(true);
      expect(
        equalBytes(
          leafPrivKeyMap.get(nodeToSend.id)!,
          await userConfig.signer.getPublicKeyFromDerivation(newKeyDerivation),
        ),
      ).toBe(true);

      expect(receiverTransfer.leaves[0]!.leaf).toBeDefined();

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

      await sspTransferService.claimTransfer(receiverTransfer, claimingNodes);
    }, 60000);

    it(`${name} - test send lightning v2 payment`, async () => {
      const faucet = BitcoinFaucet.getInstance();

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c",
      );
      const paymentHash = sha256(preimage);

      const leafId = uuidv7();
      const nodeToSend = await createTree(userWallet, leafId, faucet, 12345n);

      const newKeyDerivation: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: uuidv7(),
      };

      const leaves: LeafKeyTweak[] = [
        {
          leaf: nodeToSend,
          keyDerivation: {
            type: KeyDerivationType.LEAF,
            path: leafId,
          },
          newKeyDerivation,
        },
      ];

      const response = await lightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: await sspConfig.signer.getIdentityPublicKey(),
        paymentHash,
        isInboundPayment: false,
        invoiceString: (await fakeInvoiceCreator()).invoice.encodedInvoice,
      });

      expect(response.transfer).toBeDefined();

      const transfer = await transferService.deliverTransferPackage(
        response.transfer!,
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

        if (leaf.intermediateDirectRefundTx.length > 0) {
          const directRefund = getTxFromRawTxBytes(
            leaf.intermediateDirectRefundTx,
          );
          expectedValue += directRefund.getOutput(0)?.amount || 0n;
        }

        if (leaf.intermediateDirectFromCpfpRefundTx.length > 0) {
          const directFromCpfpRefund = getTxFromRawTxBytes(
            leaf.intermediateDirectFromCpfpRefundTx,
          );
          expectedValue += directFromCpfpRefund.getOutput(0)?.amount || 0n;
        }
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

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.has(nodeToSend.id)).toBe(true);
      expect(
        equalBytes(
          leafPrivKeyMap.get(nodeToSend.id)!,
          await userConfig.signer.getPublicKeyFromDerivation(newKeyDerivation),
        ),
      ).toBe(true);

      expect(receiverTransfer.leaves[0]!.leaf).toBeDefined();

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

      await sspTransferService.claimTransfer(receiverTransfer, claimingNodes);
    }, 60000);
  },
);
