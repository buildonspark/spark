import { beforeEach, describe, expect, it, jest } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  QueryNodesRequest,
  QueryNodesResponse,
  TreeNode,
} from "../../proto/spark.js";
import LeafManager from "../../services/leaf-manager.js";
import { KeyDerivation, KeyDerivationType } from "../../signer/types.js";
import { addPublicKeys } from "../../utils/keys.js";

class TestableLeafManager extends LeafManager {
  async queryNodesPublic(
    baseRequest: Omit<QueryNodesRequest, "limit" | "offset">,
    sparkClientAddress?: string,
    pageSize?: number,
  ): Promise<QueryNodesResponse> {
    return (this as any).queryNodes(baseRequest, sparkClientAddress, pageSize);
  }

  verifyKeyPublic(
    pubkey1: Uint8Array,
    pubkey2: Uint8Array,
    verifyingKey: Uint8Array,
  ): boolean {
    return (this as any).verifyKey(pubkey1, pubkey2, verifyingKey);
  }

  isLeafConsistentPublic(
    leaf: TreeNode,
    opLeaf: TreeNode | undefined,
  ): boolean {
    return (this as any).isLeafConsistent(leaf, opLeaf);
  }

  async recoverLeavesPublic(
    leaves: TreeNode[],
    keyDerivation: KeyDerivation,
  ): Promise<TreeNode[]> {
    return (this as any).recoverLeaves(leaves, keyDerivation);
  }

  async checkRenewLeavesPublic(nodes: TreeNode[]): Promise<TreeNode[]> {
    return (this as any).checkRenewLeaves(nodes);
  }
}
interface MockConfig {
  getCoordinatorAddress?: () => string;
  signer?: {
    getIdentityPublicKey: () => Promise<Uint8Array>;
  };
}

interface MockTransferService {
  sendTransferWithKeyTweaks?: jest.Mock;
  queryTransfer?: jest.Mock;
  claimTransfer?: jest.Mock;
  renewNodeTxn?: jest.Mock;
  renewRefundTxn?: jest.Mock;
  renewZeroTimelockNodeTxn?: jest.Mock;
}

interface MockConnectionManager {
  createSparkClient?: jest.Mock;
}

function createTestableLeafManager(overrides?: {
  config?: MockConfig;
  transferService?: MockTransferService;
  connectionManager?: MockConnectionManager;
}): TestableLeafManager {
  return new TestableLeafManager(
    (overrides?.config ?? {}) as any,
    {} as any, // swapService — unused in current tests
    (overrides?.transferService ?? {}) as any,
    (overrides?.connectionManager ?? {}) as any,
  );
}

function createMockTreeNode(overrides: Partial<TreeNode> = {}): TreeNode {
  return {
    id: "node-1",
    treeId: "tree-1",
    value: 1000,
    nodeTx: new Uint8Array(32).fill(1),
    refundTx: new Uint8Array(32).fill(2),
    vout: 0,
    verifyingPublicKey: new Uint8Array(33).fill(0),
    ownerIdentityPublicKey: new Uint8Array(33).fill(0),
    signingKeyshare: undefined,
    status: "AVAILABLE",
    network: 0,
    createdTime: undefined,
    updatedTime: undefined,
    ownerSigningPublicKey: new Uint8Array(33).fill(0),
    directTx: new Uint8Array(0),
    ...overrides,
  } as TreeNode;
}

/**
 * Build a minimal valid raw Bitcoin transaction with a specific input sequence.
 * The sequence's lower 16 bits are used as the timelock by getCurrentTimelock().
 */
function buildRawTx(inputSequence: number): Uint8Array {
  // Non-segwit: version(4) + inCount(1) + prevTxid(32) + prevVout(4)
  //   + scriptSigLen(1) + sequence(4) + outCount(1) + value(8)
  //   + scriptPubKeyLen(1) + scriptPubKey(22) + locktime(4) = 82 bytes
  const buf = new ArrayBuffer(82);
  const view = new DataView(buf);
  const arr = new Uint8Array(buf);
  view.setUint32(0, 2, true); // version 2
  arr[4] = 1; // 1 input
  // prevTxid (32 zero bytes at offset 5) + prevVout (0 at offset 37) already zero
  // scriptSig length = 0 at offset 41 already zero
  view.setUint32(42, inputSequence, true); // sequence
  arr[46] = 1; // 1 output
  view.setBigUint64(47, BigInt(1000), true); // value
  arr[55] = 22; // scriptPubKey length
  arr[56] = 0x00; // OP_0
  arr[57] = 0x14; // push 20 bytes (P2WPKH)
  // remaining scriptPubKey + locktime already zero
  return arr;
}

describe("LeafManager Test", () => {
  describe("queryNodes pagination", () => {
    let leafManager: TestableLeafManager;
    let createSparkClientMock: jest.Mock;

    beforeEach(() => {
      const paginatedResponses: Record<number, unknown> = {
        0: {
          nodes: {
            n1: { id: "n1" },
            n2: { id: "n2" },
          },
          offset: 0,
        },
        2: {
          nodes: {
            n2: { id: "n2" },
            n3: { id: "n3" },
          },
          offset: 2,
        },
        4: {
          nodes: {},
          offset: 4,
        },
      };

      const queryNodesStub = jest.fn(async ({ offset }: { offset: number }) => {
        return paginatedResponses[offset] ?? { nodes: {}, offset };
      });

      createSparkClientMock = jest.fn(async () => ({
        query_nodes: queryNodesStub,
      }));

      leafManager = createTestableLeafManager({
        config: { getCoordinatorAddress: () => "mock-address" },
        connectionManager: { createSparkClient: createSparkClientMock },
      });
    });

    it("aggregates all pages and removes duplicates", async () => {
      const result = await leafManager.queryNodesPublic(
        { includeParents: false } as Omit<
          QueryNodesRequest,
          "limit" | "offset"
        >,
        undefined,
        2,
      );

      expect(Object.keys(result.nodes)).toHaveLength(3);
      expect(Object.keys(result.nodes)).toEqual(
        expect.arrayContaining(["n1", "n2", "n3"]),
      );
      expect(result.offset).toBe(4);
      expect(createSparkClientMock).toHaveBeenCalledTimes(3);
    });
  });
  describe("verifyKey", () => {
    it("returns true when pubkey1 + pubkey2 equals verifyingKey", () => {
      const privA = secp256k1.utils.randomSecretKey();
      const privB = secp256k1.utils.randomSecretKey();
      const pubA = secp256k1.getPublicKey(privA, true);
      const pubB = secp256k1.getPublicKey(privB, true);
      const verifyingKey = addPublicKeys(pubA, pubB);

      const leafManager = createTestableLeafManager();
      expect(leafManager.verifyKeyPublic(pubA, pubB, verifyingKey)).toBe(true);
    });

    it("returns false when verifyingKey does not match the sum", () => {
      const privA = secp256k1.utils.randomSecretKey();
      const privB = secp256k1.utils.randomSecretKey();
      const pubA = secp256k1.getPublicKey(privA, true);
      const pubB = secp256k1.getPublicKey(privB, true);

      const privC = secp256k1.utils.randomSecretKey();
      const wrongVerifyingKey = secp256k1.getPublicKey(privC, true);

      const leafManager = createTestableLeafManager();
      expect(leafManager.verifyKeyPublic(pubA, pubB, wrongVerifyingKey)).toBe(
        false,
      );
    });
  });
  describe("isLeafConsistent", () => {
    const sharedSigningKeyshare = {
      ownerIdentifiers: ["op1"],
      threshold: 2,
      publicKey: new Uint8Array(33).fill(0xaa),
      publicShares: {},
      updatedTime: undefined,
    };
    const sharedNodeTx = new Uint8Array(32).fill(0xbb);

    it("returns true for identical leaves", () => {
      const leaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });
      const opLeaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, opLeaf)).toBe(true);
    });

    it("returns false when opLeaf is undefined", () => {
      const leaf = createMockTreeNode({
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, undefined)).toBe(false);
    });

    it("returns false when statuses differ", () => {
      const leaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });
      const opLeaf = createMockTreeNode({
        status: "SPENT",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, opLeaf)).toBe(false);
    });

    it("returns false when leaf is missing signingKeyshare", () => {
      const leaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: undefined,
        nodeTx: sharedNodeTx,
      });
      const opLeaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, opLeaf)).toBe(false);
    });

    it("returns false when opLeaf is missing signingKeyshare", () => {
      const leaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });
      const opLeaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: undefined,
        nodeTx: sharedNodeTx,
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, opLeaf)).toBe(false);
    });

    it("returns false when signingKeyshare publicKeys differ", () => {
      const leaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });
      const opLeaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: {
          ...sharedSigningKeyshare,
          publicKey: new Uint8Array(33).fill(0xcc),
        },
        nodeTx: sharedNodeTx,
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, opLeaf)).toBe(false);
    });

    it("returns false when nodeTx bytes differ", () => {
      const leaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: new Uint8Array(32).fill(0x01),
      });
      const opLeaf = createMockTreeNode({
        status: "AVAILABLE",
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: new Uint8Array(32).fill(0x02),
      });

      const leafManager = createTestableLeafManager();
      expect(leafManager.isLeafConsistentPublic(leaf, opLeaf)).toBe(false);
    });
  });

  describe("recoverLeaves", () => {
    it("sends a self-transfer and claims the result", async () => {
      const fakeIdentityPubkey = new Uint8Array(33).fill(0x02);
      const recoveredNode = createMockTreeNode({ id: "recovered-1" });

      const mockTransfer = { id: "transfer-1" };
      const mockPendingTransfer = { id: "transfer-1", status: "PENDING" };

      const sendTransferWithKeyTweaksMock = jest.fn(async () => mockTransfer);
      const queryTransferMock = jest.fn(async () => mockPendingTransfer);
      const claimTransferMock = jest.fn(async () => [recoveredNode]);

      const leafManager = createTestableLeafManager({
        config: {
          signer: {
            getIdentityPublicKey: jest.fn(async () => fakeIdentityPubkey),
          },
        },
        transferService: {
          sendTransferWithKeyTweaks: sendTransferWithKeyTweaksMock,
          queryTransfer: queryTransferMock,
          claimTransfer: claimTransferMock,
        },
      });

      const inputLeaf = createMockTreeNode({ id: "leaf-to-recover" });
      const keyDerivation: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: "parent-id",
      };

      const result = await leafManager.recoverLeavesPublic(
        [inputLeaf],
        keyDerivation,
      );

      // Verify sendTransferWithKeyTweaks was called with the correct leaf key tweaks.
      expect(sendTransferWithKeyTweaksMock).toHaveBeenCalledTimes(1);
      expect(sendTransferWithKeyTweaksMock).toHaveBeenCalledWith(
        [
          expect.objectContaining({
            leaf: inputLeaf,
            keyDerivation,
            newKeyDerivation: { type: KeyDerivationType.RANDOM },
          }),
        ],
        fakeIdentityPubkey,
      );

      // Verify queryTransfer was called with the transfer id.
      expect(queryTransferMock).toHaveBeenCalledWith("transfer-1");

      // Verify claimTransfer was called with the pending transfer.
      expect(claimTransferMock).toHaveBeenCalledWith(mockPendingTransfer);

      // Should return the recovered node.
      expect(result).toEqual([recoveredNode]);
    });

    it("returns empty array when queryTransfer returns null", async () => {
      const fakeIdentityPubkey = new Uint8Array(33).fill(0x02);

      const leafManager = createTestableLeafManager({
        config: {
          signer: {
            getIdentityPublicKey: jest.fn(async () => fakeIdentityPubkey),
          },
        },
        transferService: {
          sendTransferWithKeyTweaks: jest.fn(async () => ({ id: "t-1" })),
          queryTransfer: jest.fn(async () => null),
          claimTransfer: jest.fn(),
        },
      });

      const keyDerivation: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: "p",
      };
      const result = await leafManager.recoverLeavesPublic(
        [createMockTreeNode()],
        keyDerivation,
      );

      expect(result).toEqual([]);
    });
  });

  describe("checkRenewLeaves", () => {
    // Sequence values: getCurrentTimelock(seq) = seq & 0xffff
    // doesTxnNeedRenewed: timelock < 200
    // isZeroTimelock: timelock === 0
    const VALID_SEQ = 500; // timelock 500, no renewal
    const NEEDS_RENEWAL_SEQ = 100; // timelock 100, needs renewal
    const ZERO_SEQ = 0; // timelock 0, zero timelock

    function nodeWithSeqs(
      id: string,
      nodeSeq: number,
      refundSeq: number,
      parentNodeId?: string,
    ): TreeNode {
      return createMockTreeNode({
        id,
        parentNodeId,
        nodeTx: buildRawTx(nodeSeq),
        refundTx: buildRawTx(refundSeq),
      });
    }

    it("returns all nodes when none need renewal", async () => {
      const nodes = [
        nodeWithSeqs("a", VALID_SEQ, VALID_SEQ),
        nodeWithSeqs("b", VALID_SEQ, VALID_SEQ),
      ];

      const leafManager = createTestableLeafManager();
      const result = await leafManager.checkRenewLeavesPublic(nodes);

      expect(result).toEqual(nodes);
    });

    it("calls the correct renewal method for each category", async () => {
      const renewedNode = createMockTreeNode({ id: "renewed-node" });
      const renewedRefund = createMockTreeNode({ id: "renewed-refund" });
      const renewedZero = createMockTreeNode({ id: "renewed-zero" });

      // refund needs renewal + node needs renewal → renewNodeTxn
      const nodeRenewNode = nodeWithSeqs(
        "n1",
        NEEDS_RENEWAL_SEQ,
        NEEDS_RENEWAL_SEQ,
        "parent-1",
      );
      // refund needs renewal + node valid → renewRefundTxn
      const nodeRenewRefund = nodeWithSeqs(
        "n2",
        VALID_SEQ,
        NEEDS_RENEWAL_SEQ,
        "parent-2",
      );
      // refund needs renewal + node zero timelock → renewZeroTimelockNodeTxn
      const nodeRenewZero = nodeWithSeqs("n3", ZERO_SEQ, NEEDS_RENEWAL_SEQ);
      // valid
      const validNode = nodeWithSeqs("n4", VALID_SEQ, VALID_SEQ);

      const parentNode1 = createMockTreeNode({ id: "parent-1" });
      const parentNode2 = createMockTreeNode({ id: "parent-2" });

      const queryNodesStub = jest.fn(async () => ({
        nodes: {
          n1: nodeRenewNode,
          n2: nodeRenewRefund,
          n3: nodeRenewZero,
          "parent-1": parentNode1,
          "parent-2": parentNode2,
        },
        offset: 0,
      }));
      const createSparkClientMock = jest.fn(async () => ({
        query_nodes: queryNodesStub,
      }));

      const renewNodeTxnMock = jest.fn(async () => renewedNode);
      const renewRefundTxnMock = jest.fn(async () => renewedRefund);
      const renewZeroTimelockNodeTxnMock = jest.fn(async () => renewedZero);

      const leafManager = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock-addr",
          getNetworkProto: () => 0,
        } as any,
        connectionManager: { createSparkClient: createSparkClientMock },
        transferService: {
          renewNodeTxn: renewNodeTxnMock,
          renewRefundTxn: renewRefundTxnMock,
          renewZeroTimelockNodeTxn: renewZeroTimelockNodeTxnMock,
        },
      });

      const result = await leafManager.checkRenewLeavesPublic([
        nodeRenewNode,
        nodeRenewRefund,
        nodeRenewZero,
        validNode,
      ]);

      expect(renewNodeTxnMock).toHaveBeenCalledTimes(1);
      expect(renewNodeTxnMock).toHaveBeenCalledWith(nodeRenewNode, parentNode1);
      expect(renewRefundTxnMock).toHaveBeenCalledTimes(1);
      expect(renewRefundTxnMock).toHaveBeenCalledWith(
        nodeRenewRefund,
        parentNode2,
      );
      expect(renewZeroTimelockNodeTxnMock).toHaveBeenCalledTimes(1);
      expect(renewZeroTimelockNodeTxnMock).toHaveBeenCalledWith(nodeRenewZero);

      expect(result).toHaveLength(4);
      expect(result).toEqual(
        expect.arrayContaining([
          validNode,
          renewedNode,
          renewedRefund,
          renewedZero,
        ]),
      );
    });

    it("returns valid and successfully renewed nodes when one renewal fails", async () => {
      const renewedRefund = createMockTreeNode({ id: "renewed-refund" });

      // Will fail renewal
      const failNode = nodeWithSeqs(
        "fail",
        NEEDS_RENEWAL_SEQ,
        NEEDS_RENEWAL_SEQ,
        "parent-fail",
      );
      // Will succeed renewal
      const okNode = nodeWithSeqs(
        "ok",
        VALID_SEQ,
        NEEDS_RENEWAL_SEQ,
        "parent-ok",
      );
      // Already valid
      const validNode = nodeWithSeqs("valid", VALID_SEQ, VALID_SEQ);

      const parentFail = createMockTreeNode({ id: "parent-fail" });
      const parentOk = createMockTreeNode({ id: "parent-ok" });

      const queryNodesStub = jest.fn(async () => ({
        nodes: {
          fail: failNode,
          ok: okNode,
          "parent-fail": parentFail,
          "parent-ok": parentOk,
        },
        offset: 0,
      }));

      const leafManager = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock-addr",
          getNetworkProto: () => 0,
        } as any,
        connectionManager: {
          createSparkClient: jest.fn(async () => ({
            query_nodes: queryNodesStub,
          })),
        },
        transferService: {
          renewNodeTxn: jest.fn(async () => {
            throw new Error("network failure");
          }),
          renewRefundTxn: jest.fn(async () => renewedRefund),
          renewZeroTimelockNodeTxn: jest.fn(),
        },
      });

      const result = await leafManager.checkRenewLeavesPublic([
        failNode,
        okNode,
        validNode,
      ]);

      expect(result).toHaveLength(2);
      expect(result).toEqual(
        expect.arrayContaining([validNode, renewedRefund]),
      );
    });
  });
});
