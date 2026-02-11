import { describe, expect, it } from "@jest/globals";
import { hexToBytes } from "@noble/curves/utils";
import {
  Network,
  Transfer,
  TransferLeaf,
  TransferStatus,
  TransferType,
  TreeNode,
  WalletSetting,
} from "../proto/spark.js";
import {
  mapSettingsProtoToWalletSettings,
  mapTransferLeafToWalletTransferLeaf,
  mapTransferToWalletTransfer,
  mapTreeNodeToWalletLeaf,
} from "../types/sdk-types.js";

describe("sdk-types mapping functions", () => {
  describe("mapTransferToWalletTransfer", () => {
    it("should map sparkInvoice when present", () => {
      const identityPublicKey = "abc123";
      const proto: Transfer = {
        id: "transfer-1",
        senderIdentityPublicKey: hexToBytes("02abcd"),
        receiverIdentityPublicKey: hexToBytes("03ef12"),
        status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        totalValue: 1000,
        expiryTime: undefined,
        leaves: [],
        createdTime: undefined,
        updatedTime: undefined,
        type: TransferType.TRANSFER,
        sparkInvoice: "spark1testinvoice",
        network: Network.REGTEST,
      };

      const result = mapTransferToWalletTransfer(proto, identityPublicKey);

      expect(result.sparkInvoice).toBe("spark1testinvoice");
    });

    it("should map sparkInvoice to undefined when empty string", () => {
      const identityPublicKey = "abc123";
      const proto: Transfer = {
        id: "transfer-1",
        senderIdentityPublicKey: hexToBytes("02abcd"),
        receiverIdentityPublicKey: hexToBytes("03ef12"),
        status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        totalValue: 1000,
        expiryTime: undefined,
        leaves: [],
        createdTime: undefined,
        updatedTime: undefined,
        type: TransferType.TRANSFER,
        sparkInvoice: "",
        network: Network.REGTEST,
      };

      const result = mapTransferToWalletTransfer(proto, identityPublicKey);

      expect(result.sparkInvoice).toBeUndefined();
    });

    it("should map all transfer fields correctly", () => {
      const identityPublicKey = "abc123";
      const createdTime = new Date("2024-01-01T00:00:00Z");
      const updatedTime = new Date("2024-01-02T00:00:00Z");
      const expiryTime = new Date("2024-01-03T00:00:00Z");

      const proto: Transfer = {
        id: "transfer-1",
        senderIdentityPublicKey: hexToBytes("02abcd"),
        receiverIdentityPublicKey: hexToBytes("03ef12"),
        status: TransferStatus.TRANSFER_STATUS_COMPLETED,
        totalValue: 5000,
        expiryTime,
        leaves: [],
        createdTime,
        updatedTime,
        type: TransferType.TRANSFER,
        sparkInvoice: "spark1invoice123",
        network: Network.MAINNET,
      };

      const result = mapTransferToWalletTransfer(proto, identityPublicKey);

      expect(result.id).toBe("transfer-1");
      expect(result.senderIdentityPublicKey).toBe("02abcd");
      expect(result.receiverIdentityPublicKey).toBe("03ef12");
      expect(result.status).toBe("TRANSFER_STATUS_COMPLETED");
      expect(result.totalValue).toBe(5000);
      expect(result.expiryTime).toEqual(expiryTime);
      expect(result.createdTime).toEqual(createdTime);
      expect(result.updatedTime).toEqual(updatedTime);
      expect(result.type).toBe("TRANSFER");
      expect(result.sparkInvoice).toBe("spark1invoice123");
    });

    it("should set transferDirection to INCOMING when receiver matches identity", () => {
      const identityPublicKey = "03ef12";
      const proto: Transfer = {
        id: "transfer-1",
        senderIdentityPublicKey: hexToBytes("02abcd"),
        receiverIdentityPublicKey: hexToBytes("03ef12"),
        status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        totalValue: 1000,
        expiryTime: undefined,
        leaves: [],
        createdTime: undefined,
        updatedTime: undefined,
        type: TransferType.TRANSFER,
        sparkInvoice: "",
        network: Network.REGTEST,
      };

      const result = mapTransferToWalletTransfer(proto, identityPublicKey);

      expect(result.transferDirection).toBe("INCOMING");
    });

    it("should set transferDirection to OUTGOING when receiver does not match identity", () => {
      const identityPublicKey = "02abcd";
      const proto: Transfer = {
        id: "transfer-1",
        senderIdentityPublicKey: hexToBytes("02abcd"),
        receiverIdentityPublicKey: hexToBytes("03ef12"),
        status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        totalValue: 1000,
        expiryTime: undefined,
        leaves: [],
        createdTime: undefined,
        updatedTime: undefined,
        type: TransferType.TRANSFER,
        sparkInvoice: "",
        network: Network.REGTEST,
      };

      const result = mapTransferToWalletTransfer(proto, identityPublicKey);

      expect(result.transferDirection).toBe("OUTGOING");
    });
  });

  describe("mapTransferLeafToWalletTransferLeaf", () => {
    it("should map transfer leaf fields correctly", () => {
      const proto: TransferLeaf = {
        leaf: undefined,
        secretCipher: hexToBytes("deadbeef"),
        signature: hexToBytes("cafebabe"),
        intermediateRefundTx: hexToBytes("123456"),
        intermediateDirectRefundTx: new Uint8Array(),
        intermediateDirectFromCpfpRefundTx: new Uint8Array(),
        pendingKeyTweakPublicKey: new Uint8Array(),
      };

      const result = mapTransferLeafToWalletTransferLeaf(proto);

      expect(result.leaf).toBeUndefined();
      expect(result.secretCipher).toBe("deadbeef");
      expect(result.signature).toBe("cafebabe");
      expect(result.intermediateRefundTx).toBe("123456");
    });
  });

  describe("mapTreeNodeToWalletLeaf", () => {
    it("should map tree node fields correctly", () => {
      const proto: TreeNode = {
        id: "node-1",
        treeId: "tree-1",
        value: 1000,
        parentNodeId: "parent-1",
        nodeTx: hexToBytes("abcd1234"),
        refundTx: hexToBytes("ef567890"),
        vout: 0,
        verifyingPublicKey: hexToBytes("02abcd"),
        ownerIdentityPublicKey: hexToBytes("03ef12"),
        signingKeyshare: undefined,
        status: "ACTIVE",
        network: Network.REGTEST,
        createdTime: undefined,
        updatedTime: undefined,
        ownerSigningPublicKey: hexToBytes("04abcd"),
        directTx: hexToBytes("abcd1234"),
        directRefundTx: hexToBytes("ef567890"),
        directFromCpfpRefundTx: hexToBytes("ded12345"),
      };

      const result = mapTreeNodeToWalletLeaf(proto);

      expect(result.id).toBe("node-1");
      expect(result.treeId).toBe("tree-1");
      expect(result.value).toBe(1000);
      expect(result.parentNodeId).toBe("parent-1");
      expect(result.nodeTx).toBe("abcd1234");
      expect(result.refundTx).toBe("ef567890");
      expect(result.vout).toBe(0);
      expect(result.verifyingPublicKey).toBe("02abcd");
      expect(result.ownerIdentityPublicKey).toBe("03ef12");
      expect(result.network).toBe("REGTEST");
    });
  });

  describe("mapSettingsProtoToWalletSettings", () => {
    it("should map wallet settings correctly", () => {
      const proto: WalletSetting = {
        ownerIdentityPublicKey: hexToBytes("03ef12"),
        privateEnabled: true,
      };

      const result = mapSettingsProtoToWalletSettings(proto);

      expect(result.ownerIdentityPublicKey).toBe("03ef12");
      expect(result.privateEnabled).toBe(true);
    });
  });
});
