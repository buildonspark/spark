import { beforeEach, describe, expect, it, jest } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  QueryNodesRequest,
  QueryNodesResponse,
  TransferType,
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

  transitionPublic(
    leafIds: string[],
    toStatus: string,
    meta?: { source: unknown },
  ): void {
    (this as any).transition(leafIds, toStatus, meta);
  }

  getLeafRecordPublic(
    id: string,
  ): { treeNode: TreeNode; status: string; source: unknown } | undefined {
    return (this as any).leaves.get(id);
  }

  getLeafCount(): number {
    return (this as any).leaves.size;
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
  queryPendingTransfers?: jest.Mock;
  queryPrimarySwapTransfers?: jest.Mock;
  queryCounterSwapTransfers?: jest.Mock;
  queryPendingOutgoingTransfers?: jest.Mock;
}

interface MockConnectionManager {
  createSparkClient?: jest.Mock;
}

function createTestableLeafManager(overrides?: {
  config?: MockConfig;
  transferService?: MockTransferService;
  connectionManager?: MockConnectionManager;
  onBalanceUpdate?: (balance: {
    available: number;
    owned: number;
    incoming: number;
  }) => void;
}): TestableLeafManager {
  return new TestableLeafManager(
    (overrides?.config ?? {}) as any,
    {} as any, // swapService — unused in current tests
    (overrides?.transferService ?? {}) as any,
    (overrides?.connectionManager ?? {}) as any,
    overrides?.onBalanceUpdate,
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

  // ── Cache & State Management ─────────────────────────────────────────

  describe("addLeaves", () => {
    it("adds leaves as AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);

      expect(lm.getAvailableBalance()).toBe(800);
      expect(lm.getOwnedBalance()).toBe(800);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("overwrites existing leaf with same id", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 900 })]);

      expect(lm.getAvailableBalance()).toBe(900);
      expect(lm.getLeafCount()).toBe(1);
    });

    it("does not overwrite LOCAL_LOCKED leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 900 })]);

      expect(lm.getLeafRecordPublic("a")?.status).toBe("LOCAL_LOCKED");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(500);
    });

    it("does not overwrite OUTGOING leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "OUTGOING");

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 900 })]);

      expect(lm.getLeafRecordPublic("a")?.status).toBe("OUTGOING");
    });

    it("does not overwrite SWAP_PENDING leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "SWAP_PENDING");

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 900 })]);

      expect(lm.getLeafRecordPublic("a")?.status).toBe("SWAP_PENDING");
    });
  });

  describe("addIncomingLeaves", () => {
    it("adds leaves as INCOMING", async () => {
      const lm = createTestableLeafManager();
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 500 })],
        "t-1",
      );

      expect(lm.getIncomingBalance()).toBe(500);
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("does not overwrite non-INCOMING leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 500 })],
        "t-1",
      );

      expect(lm.getAvailableBalance()).toBe(500);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("overwrites existing INCOMING leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 500 })],
        "t-1",
      );
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 700 })],
        "t-2",
      );

      expect(lm.getIncomingBalance()).toBe(700);
      expect(lm.getLeafCount()).toBe(1);
    });
  });

  describe("removeLeaves", () => {
    it("removes leaves from cache", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);
      await lm.removeLeaves(["a"]);

      expect(lm.getAvailableBalance()).toBe(300);
      expect(lm.getLeafCount()).toBe(1);
    });

    it("ignores non-existent ids", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      await lm.removeLeaves(["missing"]);

      expect(lm.getAvailableBalance()).toBe(500);
    });
  });

  // ── Transition State Machine ─────────────────────────────────────────

  describe("transition", () => {
    it("AVAILABLE → LOCAL_LOCKED", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      lm.transitionPublic(["a"], "LOCAL_LOCKED");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("LOCAL_LOCKED");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(1000);
    });

    it("LOCAL_LOCKED → AVAILABLE | OUTGOING | SWAP_PENDING", async () => {
      for (const target of ["AVAILABLE", "OUTGOING", "SWAP_PENDING"]) {
        const lm = createTestableLeafManager();
        await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
        lm.transitionPublic(["a"], "LOCAL_LOCKED");

        lm.transitionPublic(["a"], target);

        expect(lm.getLeafRecordPublic("a")?.status).toBe(target);
      }
    });

    it("OUTGOING → AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "OUTGOING");

      lm.transitionPublic(["a"], "AVAILABLE");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
    });

    it("OUTGOING → SPENT deletes the leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "OUTGOING");

      lm.transitionPublic(["a"], "SPENT");

      expect(lm.getLeafRecordPublic("a")).toBeUndefined();
    });

    it("SWAP_PENDING → AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "SWAP_PENDING");

      lm.transitionPublic(["a"], "AVAILABLE");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
    });

    it("SWAP_PENDING → SPENT deletes the leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "SWAP_PENDING");

      lm.transitionPublic(["a"], "SPENT");

      expect(lm.getLeafRecordPublic("a")).toBeUndefined();
    });

    it("INCOMING → AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 1000 })],
        "t-1",
      );

      lm.transitionPublic(["a"], "AVAILABLE");

      expect(lm.getAvailableBalance()).toBe(1000);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("rejects invalid transitions", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      // AVAILABLE → SPENT is invalid (must go through LOCAL_LOCKED first)
      lm.transitionPublic(["a"], "SPENT");
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");

      // AVAILABLE → OUTGOING is invalid
      lm.transitionPublic(["a"], "OUTGOING");
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");

      // AVAILABLE → SWAP_PENDING is invalid
      lm.transitionPublic(["a"], "SWAP_PENDING");
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");

      // AVAILABLE → INCOMING is invalid
      lm.transitionPublic(["a"], "INCOMING");
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
    });

    it("SPENT deletes the leaf — further transitions are no-ops", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "OUTGOING");
      lm.transitionPublic(["a"], "SPENT");

      // Leaf is deleted
      expect(lm.getLeafRecordPublic("a")).toBeUndefined();

      // Further transitions on deleted leaf are silent no-ops
      for (const target of [
        "AVAILABLE",
        "LOCAL_LOCKED",
        "OUTGOING",
        "SWAP_PENDING",
        "INCOMING",
      ]) {
        lm.transitionPublic(["a"], target);
      }

      expect(lm.getLeafRecordPublic("a")).toBeUndefined();
    });

    it("skips unknown leaf ids without error", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      lm.transitionPublic(["missing", "a"], "LOCAL_LOCKED");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("LOCAL_LOCKED");
      expect(lm.getLeafRecordPublic("missing")).toBeUndefined();
    });

    it("updates source metadata when provided", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      lm.transitionPublic(["a"], "LOCAL_LOCKED", {
        source: { kind: "transfer", transferId: "t-1" },
      });

      expect(lm.getLeafRecordPublic("a")?.source).toEqual({
        kind: "transfer",
        transferId: "t-1",
      });
    });

    it("does not change source when meta is omitted", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      lm.transitionPublic(["a"], "LOCAL_LOCKED");

      // source should remain the default from addLeaves ({ kind: "none" })
      expect(lm.getLeafRecordPublic("a")?.source).toEqual({ kind: "none" });
    });
  });

  // ── Balance Calculations ─────────────────────────────────────────────

  describe("balance calculations", () => {
    it("returns zero for empty cache", () => {
      const lm = createTestableLeafManager();
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("getOwnedBalance includes AVAILABLE + LOCAL_LOCKED + OUTGOING + SWAP_PENDING", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 100 }),
        createMockTreeNode({ id: "b", value: 200 }),
        createMockTreeNode({ id: "c", value: 300 }),
        createMockTreeNode({ id: "d", value: 400 }),
      ]);

      lm.transitionPublic(["b"], "LOCAL_LOCKED");
      lm.transitionPublic(["c"], "LOCAL_LOCKED");
      lm.transitionPublic(["c"], "OUTGOING");
      lm.transitionPublic(["d"], "LOCAL_LOCKED");
      lm.transitionPublic(["d"], "SWAP_PENDING");

      expect(lm.getAvailableBalance()).toBe(100);
      expect(lm.getOwnedBalance()).toBe(1000);
    });

    it("getOwnedBalance excludes SPENT and INCOMING", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "b", value: 300 })],
        "t-1",
      );

      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "OUTGOING");
      lm.transitionPublic(["a"], "SPENT");

      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("getIncomingBalance sums only INCOMING leaves", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "b", value: 300 })],
        "t-1",
      );

      expect(lm.getIncomingBalance()).toBe(300);
    });
  });

  // ── Sync ─────────────────────────────────────────────────────────────

  describe("sync", () => {
    function mockSyncDeps(
      lm: TestableLeafManager,
      opts: {
        leaves?: TreeNode[];
        swaps?: { leaf: TreeNode; transferId: string }[];
        outgoing?: { leaf: TreeNode; transferId: string }[];
        incomingTransfers?: {
          id: string;
          type: number;
          leaves: { leaf: TreeNode }[];
        }[];
      },
    ) {
      (lm as any).getLeaves = jest.fn(async () => opts.leaves ?? []);
      (lm as any).getAllPendingSwaps = jest.fn(async () => opts.swaps ?? []);
      (lm as any).getAllPendingOutgoingTransfers = jest.fn(
        async () => opts.outgoing ?? [],
      );
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );
      (lm as any).transferService.queryPendingTransfers = jest.fn(async () => ({
        transfers: opts.incomingTransfers ?? [],
      }));
    }

    it("populates cache from server data", async () => {
      const available1 = createMockTreeNode({
        id: "a1",
        value: 100,
        status: "AVAILABLE",
      });
      const available2 = createMockTreeNode({
        id: "a2",
        value: 200,
        status: "AVAILABLE",
      });
      const swapLeaf = createMockTreeNode({ id: "s1", value: 300 });
      const outgoingLeaf = createMockTreeNode({ id: "o1", value: 400 });
      const incomingLeaf = createMockTreeNode({ id: "i1", value: 500 });

      const lm = createTestableLeafManager();

      mockSyncDeps(lm, {
        leaves: [available1, available2],
        swaps: [{ leaf: swapLeaf, transferId: "swap-t1" }],
        outgoing: [{ leaf: outgoingLeaf, transferId: "out-t1" }],
        incomingTransfers: [
          {
            id: "incoming-t1",
            type: TransferType.TRANSFER, // not a counter-swap
            leaves: [{ leaf: incomingLeaf }],
          },
        ],
      });

      await lm.sync();

      expect(lm.getAvailableBalance()).toBe(300); // a1 + a2
      expect(lm.getOwnedBalance()).toBe(1000); // a1 + a2 + s1 + o1
      expect(lm.getIncomingBalance()).toBe(500); // i1
      expect(lm.getLeafRecordPublic("s1")?.status).toBe("SWAP_PENDING");
      expect(lm.getLeafRecordPublic("o1")?.status).toBe("OUTGOING");
      expect(lm.getLeafRecordPublic("i1")?.status).toBe("INCOMING");
    });

    it("preserves LOCAL_LOCKED leaves across sync", async () => {
      const lm = createTestableLeafManager({
        transferService: {
          queryPendingTransfers: jest.fn(async () => ({ transfers: [] })),
        },
      });

      // Pre-populate with a LOCAL_LOCKED leaf
      await lm.addLeaves([createMockTreeNode({ id: "locked", value: 999 })]);
      lm.transitionPublic(["locked"], "LOCAL_LOCKED");

      // Sync returns different leaves from the server
      const serverLeaf = createMockTreeNode({
        id: "server-1",
        value: 100,
        status: "AVAILABLE",
      });
      mockSyncDeps(lm, { leaves: [serverLeaf] });

      await lm.sync();

      expect(lm.getLeafRecordPublic("locked")?.status).toBe("LOCAL_LOCKED");
      expect(lm.getAvailableBalance()).toBe(100);
      expect(lm.getOwnedBalance()).toBe(1099); // server-1 + locked
    });

    it("does not preserve OUTGOING or SWAP_PENDING across sync", async () => {
      const lm = createTestableLeafManager({
        transferService: {
          queryPendingTransfers: jest.fn(async () => ({ transfers: [] })),
        },
      });

      await lm.addLeaves([
        createMockTreeNode({ id: "out", value: 500 }),
        createMockTreeNode({ id: "swap", value: 300 }),
      ]);
      lm.transitionPublic(["out"], "LOCAL_LOCKED");
      lm.transitionPublic(["out"], "OUTGOING");
      lm.transitionPublic(["swap"], "LOCAL_LOCKED");
      lm.transitionPublic(["swap"], "SWAP_PENDING");

      // Sync doesn't return these leaves from the server
      mockSyncDeps(lm, { leaves: [] });

      await lm.sync();

      expect(lm.getLeafRecordPublic("out")).toBeUndefined();
      expect(lm.getLeafRecordPublic("swap")).toBeUndefined();
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("skips counter-swap transfers in incoming", async () => {
      const counterSwapLeaf = createMockTreeNode({ id: "cs1", value: 100 });
      const counterSwapV3Leaf = createMockTreeNode({ id: "cs2", value: 150 });
      const regularLeaf = createMockTreeNode({ id: "r1", value: 200 });

      const lm = createTestableLeafManager();

      mockSyncDeps(lm, {
        leaves: [],
        incomingTransfers: [
          {
            id: "t-counter",
            type: TransferType.COUNTER_SWAP as number,
            leaves: [{ leaf: counterSwapLeaf }],
          },
          {
            id: "t-counter-v3",
            type: TransferType.COUNTER_SWAP_V3 as number,
            leaves: [{ leaf: counterSwapV3Leaf }],
          },
          {
            id: "t-regular",
            type: TransferType.TRANSFER, // not a counter-swap
            leaves: [{ leaf: regularLeaf }],
          },
        ],
      });

      await lm.sync();

      expect(lm.getLeafRecordPublic("cs1")).toBeUndefined();
      expect(lm.getLeafRecordPublic("cs2")).toBeUndefined();
      expect(lm.getIncomingBalance()).toBe(200);
    });

    it("server state overwrites stale cache (except LOCAL_LOCKED)", async () => {
      const lm = createTestableLeafManager({
        transferService: {
          queryPendingTransfers: jest.fn(async () => ({ transfers: [] })),
        },
      });

      // Pre-populate cache with some leaves
      await lm.addLeaves([
        createMockTreeNode({ id: "stale", value: 500 }),
        createMockTreeNode({ id: "kept", value: 200 }),
      ]);

      // Sync returns only "kept" — "stale" should be gone
      mockSyncDeps(lm, {
        leaves: [
          createMockTreeNode({ id: "kept", value: 200, status: "AVAILABLE" }),
        ],
      });

      await lm.sync();

      expect(lm.getLeafRecordPublic("stale")).toBeUndefined();
      expect(lm.getLeafRecordPublic("kept")).toBeDefined();
      expect(lm.getAvailableBalance()).toBe(200);
    });

    it("emits balance update after sync", async () => {
      const callback = jest.fn();
      const lm = createTestableLeafManager({
        transferService: {
          queryPendingTransfers: jest.fn(async () => ({ transfers: [] })),
        },
        onBalanceUpdate: callback,
      });

      mockSyncDeps(lm, {
        leaves: [
          createMockTreeNode({ id: "a", value: 100, status: "AVAILABLE" }),
        ],
      });

      await lm.sync();

      expect(callback).toHaveBeenCalledWith({
        available: 100,
        owned: 100,
        incoming: 0,
      });
    });
  });

  // ── onBalanceUpdate Callback ─────────────────────────────────────────

  describe("onBalanceUpdate callback", () => {
    it("fires after addLeaves", async () => {
      const callback = jest.fn();
      const lm = createTestableLeafManager({ onBalanceUpdate: callback });
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      expect(callback).toHaveBeenCalledWith({
        available: 1000,
        owned: 1000,
        incoming: 0,
      });
    });

    it("fires after removeLeaves", async () => {
      const callback = jest.fn();
      const lm = createTestableLeafManager({ onBalanceUpdate: callback });
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);
      callback.mockClear();

      await lm.removeLeaves(["a"]);

      expect(callback).toHaveBeenCalledWith({
        available: 300,
        owned: 300,
        incoming: 0,
      });
    });

    it("fires after addIncomingLeaves", async () => {
      const callback = jest.fn();
      const lm = createTestableLeafManager({ onBalanceUpdate: callback });

      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 500 })],
        "t-1",
      );

      expect(callback).toHaveBeenCalledWith({
        available: 0,
        owned: 0,
        incoming: 500,
      });
    });

    it("does not fire when no callback provided", async () => {
      const lm = createTestableLeafManager();
      // Should not throw
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      expect(lm.getAvailableBalance()).toBe(1000);
    });
  });

  // ── registerClaimedLeaves ────────────────────────────────────────────

  describe("registerClaimedLeaves", () => {
    it("overwrites SWAP_PENDING leaf to AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "SWAP_PENDING");

      const claimed = [createMockTreeNode({ id: "a", value: 500 })];
      const result = await lm.registerClaimedLeaves(claimed);

      expect(result).toHaveLength(1);
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
    });

    it("overwrites INCOMING leaf to AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "a", value: 300 })],
        "t-1",
      );
      expect(lm.getIncomingBalance()).toBe(300);

      const result = await lm.registerClaimedLeaves([
        createMockTreeNode({ id: "a", value: 300 }),
      ]);

      expect(result).toHaveLength(1);
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(300);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("emits balance update with correct values", async () => {
      const callback = jest.fn();
      const lm = createTestableLeafManager({ onBalanceUpdate: callback });
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      await lm.registerClaimedLeaves([
        createMockTreeNode({ id: "a", value: 1000 }),
      ]);

      expect(callback).toHaveBeenCalledWith({
        available: 1000,
        owned: 1000,
        incoming: 0,
      });
    });
  });
});
