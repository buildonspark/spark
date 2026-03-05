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

  selectLeavesPublic(
    targetAmounts: number[],
  ): [{ [key: number]: TreeNode[] }, boolean] {
    return (this as any).selectLeaves(targetAmounts);
  }

  determineLeavesToSwapPublic(targetAmount: number): TreeNode[] {
    return (this as any).determineLeavesToSwap(targetAmount);
  }

  restoreLocalLockedToAvailablePublic(leafIds: string[]): void {
    (this as any).restoreLocalLockedToAvailable(leafIds);
  }

  isStaleLeafErrorPublic(error: unknown): boolean {
    return (this as any).isStaleLeafError(error);
  }
}
interface MockConfig {
  getCoordinatorAddress?: () => string;
  getOptimizationOptions?: () => { auto?: boolean; multiplicity?: number };
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

interface MockSwapService {
  requestLeavesSwap?: jest.Mock;
}

interface MockConnectionManager {
  createSparkClient?: jest.Mock;
}

function createTestableLeafManager(overrides?: {
  config?: MockConfig;
  swapService?: MockSwapService;
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
    (overrides?.swapService ?? {}) as any,
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
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
        },
      });
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
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
        },
      });
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
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
        },
        onBalanceUpdate: callback,
      });
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

  // ── Leaf Selection ───────────────────────────────────────────────────

  describe("selectLeaves (greedy exact-fit)", () => {
    it("selects a single leaf that exactly matches target", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      const [results, found] = lm.selectLeavesPublic([500]);

      expect(found).toBe(true);
      expect(results[0]).toHaveLength(1);
      expect(results[0]![0]!.id).toBe("a");
    });

    it("selects multiple leaves to reach exact target", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 300 }),
        createMockTreeNode({ id: "b", value: 200 }),
      ]);

      const [results, found] = lm.selectLeavesPublic([500]);

      expect(found).toBe(true);
      expect(results[0]).toHaveLength(2);
    });

    it("returns false when exact fit is impossible", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 700 })]);

      const [results, found] = lm.selectLeavesPublic([500]);

      expect(found).toBe(false);
      expect(results[0]).toHaveLength(0);
    });

    it("returns false when not enough balance", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 300 })]);

      const [, found] = lm.selectLeavesPublic([500]);

      expect(found).toBe(false);
    });

    it("returns true with empty results for empty targets", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      const [, found] = lm.selectLeavesPublic([]);

      expect(found).toBe(true);
    });

    it("handles multiple target amounts without reusing leaves", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
        createMockTreeNode({ id: "c", value: 200 }),
      ]);

      const [results, found] = lm.selectLeavesPublic([500, 300]);

      expect(found).toBe(true);
      expect(results[0]).toHaveLength(1);
      expect(results[1]).toHaveLength(1);

      const batch0Ids = results[0]!.map((l: TreeNode) => l.id);
      const batch1Ids = results[1]!.map((l: TreeNode) => l.id);
      expect(
        batch0Ids.filter((id: string) => batch1Ids.includes(id)),
      ).toHaveLength(0);
    });

    it("fails when competing batches exhaust available leaves", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);

      const [, found] = lm.selectLeavesPublic([500, 500]);

      expect(found).toBe(false);
    });

    it("prefers larger leaves first (greedy descending)", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "small", value: 100 }),
        createMockTreeNode({ id: "big", value: 400 }),
        createMockTreeNode({ id: "medium", value: 200 }),
      ]);

      const [results, found] = lm.selectLeavesPublic([600]);

      expect(found).toBe(true);
      const ids = results[0]!.map((l: TreeNode) => l.id);
      expect(ids).toContain("big");
      expect(ids).toContain("medium");
    });

    it("finds exact fit by processing smaller targets first", async () => {
      const lm = createTestableLeafManager();
      // [200, 150, 150] with targets [300, 200]:
      // Naive descending greedy would assign 200 to target-300, fail both.
      // Ascending order processes target-200 first (gets leaf-200), then
      // target-300 gets 150+150 = 300. Both satisfied.
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 200 }),
        createMockTreeNode({ id: "b", value: 150 }),
        createMockTreeNode({ id: "c", value: 150 }),
      ]);

      const [results, found] = lm.selectLeavesPublic([300, 200]);

      expect(found).toBe(true);
      const batch0Total = results[0]!.reduce(
        (sum: number, l: TreeNode) => sum + l.value,
        0,
      );
      const batch1Total = results[1]!.reduce(
        (sum: number, l: TreeNode) => sum + l.value,
        0,
      );
      expect(batch0Total).toBe(300);
      expect(batch1Total).toBe(200);
    });

    it("only considers AVAILABLE leaves (ignores locked)", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 500 }),
      ]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");

      const [results, found] = lm.selectLeavesPublic([500]);

      expect(found).toBe(true);
      expect(results[0]![0]!.id).toBe("b");
    });

    it("skips leaf when it would overshoot the target", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "big", value: 700 }),
        createMockTreeNode({ id: "exact", value: 500 }),
      ]);

      const [results, found] = lm.selectLeavesPublic([500]);

      expect(found).toBe(true);
      expect(results[0]).toHaveLength(1);
      expect(results[0]![0]!.id).toBe("exact");
    });

    it("returns empty cache selection", async () => {
      const lm = createTestableLeafManager();

      const [results, found] = lm.selectLeavesPublic([100]);

      expect(found).toBe(false);
      expect(results[0]).toHaveLength(0);
    });
  });

  describe("determineLeavesToSwap", () => {
    it("selects smallest leaves first (ascending)", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "big", value: 1000 }),
        createMockTreeNode({ id: "small", value: 100 }),
        createMockTreeNode({ id: "medium", value: 500 }),
      ]);

      const result = lm.determineLeavesToSwapPublic(600);

      expect(result).toHaveLength(2);
      const ids = result.map((l) => l.id);
      expect(ids).toContain("small");
      expect(ids).toContain("medium");
    });

    it("throws when not enough leaves to cover target", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      expect(() => lm.determineLeavesToSwapPublic(500)).toThrow(
        "Not enough leaves to swap",
      );
    });

    it("stops as soon as target is reached", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 200 }),
        createMockTreeNode({ id: "b", value: 300 }),
        createMockTreeNode({ id: "c", value: 500 }),
      ]);

      const result = lm.determineLeavesToSwapPublic(400);

      expect(result).toHaveLength(2);
    });
  });

  describe("selectLeavesReadOnly", () => {
    it("selects largest leaves first without locking", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "small", value: 100 }),
        createMockTreeNode({ id: "big", value: 500 }),
        createMockTreeNode({ id: "medium", value: 300 }),
      ]);

      const result = lm.selectLeavesReadOnly(700);

      expect(result).toHaveLength(2);
      expect(result[0]!.id).toBe("big");
      expect(result[1]!.id).toBe("medium");
      expect(lm.getLeafRecordPublic("big")?.status).toBe("AVAILABLE");
      expect(lm.getLeafRecordPublic("medium")?.status).toBe("AVAILABLE");
    });

    it("returns all leaves if target exceeds balance", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 100 }),
        createMockTreeNode({ id: "b", value: 200 }),
      ]);

      const result = lm.selectLeavesReadOnly(1000);

      expect(result).toHaveLength(2);
    });

    it("returns empty for empty cache", () => {
      const lm = createTestableLeafManager();
      expect(lm.selectLeavesReadOnly(100)).toHaveLength(0);
    });

    it("stops early when target is met", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
        createMockTreeNode({ id: "c", value: 200 }),
      ]);

      const result = lm.selectLeavesReadOnly(500);

      expect(result).toHaveLength(1);
      expect(result[0]!.id).toBe("a");
    });
  });

  describe("restoreLocalLockedToAvailable", () => {
    it("restores LOCAL_LOCKED leaves back to AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");

      lm.restoreLocalLockedToAvailablePublic(["a"]);

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
    });

    it("does not touch leaves in other statuses", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 500 }),
      ]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");
      lm.transitionPublic(["a"], "OUTGOING");
      lm.transitionPublic(["b"], "LOCAL_LOCKED");
      lm.transitionPublic(["b"], "SWAP_PENDING");

      lm.restoreLocalLockedToAvailablePublic(["a", "b"]);

      expect(lm.getLeafRecordPublic("a")?.status).toBe("OUTGOING");
      expect(lm.getLeafRecordPublic("b")?.status).toBe("SWAP_PENDING");
    });

    it("handles non-existent ids gracefully", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);
      lm.transitionPublic(["a"], "LOCAL_LOCKED");

      lm.restoreLocalLockedToAvailablePublic(["a", "missing"]);

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
    });
  });

  describe("isStaleLeafError", () => {
    it.each([
      "Leaf is not available to transfer",
      "Leaf is not owned by this user",
      "leaf is unavailable for operation",
      "LEAF IS NOT AVAILABLE",
    ])("returns true for: %s", (msg) => {
      const lm = createTestableLeafManager();
      expect(lm.isStaleLeafErrorPublic(new Error(msg))).toBe(true);
    });

    it("returns false for unrelated errors", () => {
      const lm = createTestableLeafManager();
      expect(lm.isStaleLeafErrorPublic(new Error("network timeout"))).toBe(
        false,
      );
    });

    it("returns false for non-Error values", () => {
      const lm = createTestableLeafManager();
      expect(lm.isStaleLeafErrorPublic("string error")).toBe(false);
      expect(lm.isStaleLeafErrorPublic(null)).toBe(false);
      expect(lm.isStaleLeafErrorPublic(42)).toBe(false);
    });
  });

  describe("executeWithAllLeaves", () => {
    it("locks all available leaves and passes to executor", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);

      const result = await lm.executeWithAllLeaves(async (leaves) => {
        expect(leaves).toHaveLength(2);
        expect(lm.getLeafRecordPublic("a")?.status).toBe("LOCAL_LOCKED");
        expect(lm.getLeafRecordPublic("b")?.status).toBe("LOCAL_LOCKED");
        return "done";
      });

      expect(result).toBe("done");
    });

    it("restores leaves on executor failure", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);

      await expect(
        lm.executeWithAllLeaves(async () => {
          throw new Error("executor failed");
        }),
      ).rejects.toThrow("executor failed");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
      expect(lm.getLeafRecordPublic("b")?.status).toBe("AVAILABLE");
    });

    it("does not restore leaves executor already advanced", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
      ]);

      await expect(
        lm.executeWithAllLeaves(async () => {
          lm.transitionPublic(["a"], "OUTGOING");
          throw new Error("partial failure");
        }),
      ).rejects.toThrow("partial failure");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("OUTGOING");
      expect(lm.getLeafRecordPublic("b")?.status).toBe("AVAILABLE");
    });
  });

  describe("selectLeavesAndExecute", () => {
    it("rejects non-positive target amounts", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      await expect(
        lm.selectLeavesAndExecute([0], async () => "ok"),
      ).rejects.toThrow("Target amount must be positive");

      await expect(
        lm.selectLeavesAndExecute([-1], async () => "ok"),
      ).rejects.toThrow("Target amount must be positive");
    });

    it("rejects when total exceeds available balance", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      await expect(
        lm.selectLeavesAndExecute([600], async () => "ok"),
      ).rejects.toThrow("Total target amount exceeds available balance");
    });

    it("selects exact-fit leaves and executes without swap", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 300 }),
        createMockTreeNode({ id: "b", value: 200 }),
        createMockTreeNode({ id: "c", value: 500 }),
      ]);

      const result = await lm.selectLeavesAndExecute(
        [500],
        async (selected) => {
          expect(selected[0]).toHaveLength(1);
          expect(selected[0]![0]!.id).toBe("c");
          return "executed";
        },
      );

      expect(result).toBe("executed");
    });

    it("restores LOCAL_LOCKED leaves on executor failure", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => {
          throw new Error("executor failed");
        }),
      ).rejects.toThrow("executor failed");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
    });

    it("retries on stale leaf error after sync", async () => {
      let attempt = 0;
      const lm = createTestableLeafManager({
        transferService: {
          queryPendingTransfers: jest.fn(async () => ({ transfers: [] })),
        },
      });

      (lm as any).getLeaves = jest.fn(async () => [
        createMockTreeNode({ id: "fresh", value: 500, status: "AVAILABLE" }),
      ]);
      (lm as any).getAllPendingSwaps = jest.fn(async () => []);
      (lm as any).getAllPendingOutgoingTransfers = jest.fn(async () => []);
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      const result = await lm.selectLeavesAndExecute([500], async () => {
        attempt++;
        if (attempt === 1) {
          throw new Error("leaf is not available to transfer");
        }
        return "retried-ok";
      });

      expect(result).toBe("retried-ok");
      expect(attempt).toBe(2);
    });

    it("does not retry on non-stale errors", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => {
          throw new Error("network timeout");
        }),
      ).rejects.toThrow("network timeout");
    });

    it("triggers swap when exact fit is impossible", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (params: any) => {
            await params.onSwapInitiated?.();
            return [
              createMockTreeNode({ id: "new-500", value: 500 }),
              createMockTreeNode({ id: "new-200", value: 200 }),
            ];
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big", value: 700 })]);

      const result = await lm.selectLeavesAndExecute(
        [500],
        async (selected) => {
          expect(selected[0]).toHaveLength(1);
          expect(selected[0]![0]!.value).toBe(500);
          return "swapped-ok";
        },
      );

      expect(result).toBe("swapped-ok");
    });

    it("restores LOCAL_LOCKED leaves when swap fails before onSwapInitiated", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            throw new Error("swap service down");
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big", value: 700 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => "ok"),
      ).rejects.toThrow("swap service down");

      expect(lm.getLeafRecordPublic("big")?.status).toBe("AVAILABLE");
    });

    it("does not restore SWAP_PENDING leaves when swap fails after onSwapInitiated", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (params: any) => {
            await params.onSwapInitiated?.();
            throw new Error("swap failed mid-flight");
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big", value: 700 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => "ok"),
      ).rejects.toThrow("swap failed mid-flight");

      expect(lm.getLeafRecordPublic("big")?.status).toBe("SWAP_PENDING");
    });
  });

  describe("selectLeavesAndExecute with multiple targets", () => {
    it("selects separate batches for each target", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 200 }),
        createMockTreeNode({ id: "b", value: 300 }),
        createMockTreeNode({ id: "c", value: 500 }),
      ]);

      const result = await lm.selectLeavesAndExecute(
        [500, 300],
        async (selected) => {
          const batch0Values = selected[0]!.map((l: TreeNode) => l.value);
          const batch1Values = selected[1]!.map((l: TreeNode) => l.value);
          expect(batch0Values.reduce((a: number, b: number) => a + b, 0)).toBe(
            500,
          );
          expect(batch1Values.reduce((a: number, b: number) => a + b, 0)).toBe(
            300,
          );
          return "multi-ok";
        },
      );

      expect(result).toBe("multi-ok");
    });

    it("rejects when any individual target is non-positive", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      await expect(
        lm.selectLeavesAndExecute([500, 0], async () => "ok"),
      ).rejects.toThrow("Target amount must be positive");
    });
  });

  // ── Optimize Leaves ──────────────────────────────────────────────────

  describe("optimizeLeaves", () => {
    /** Drain an async generator, collecting yielded values. */
    async function drainOptimize(
      gen: AsyncGenerator<
        { step: number; total: number; controller: AbortController },
        void,
        void
      >,
    ) {
      const steps: { step: number; total: number }[] = [];
      for await (const { step, total } of gen) {
        steps.push({ step, total });
      }
      return steps;
    }

    it("throws on negative multiplicity", async () => {
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
      });
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      await expect(drainOptimize(lm.optimizeLeaves(-1))).rejects.toThrow(
        "Multiplicity cannot be negative",
      );
    });

    it("throws on multiplicity > 5", async () => {
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
      });
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      await expect(drainOptimize(lm.optimizeLeaves(6))).rejects.toThrow(
        "Multiplicity cannot be greater than 5",
      );
    });

    it("returns immediately when no swaps are needed", async () => {
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
      });
      // A single power-of-two leaf — already optimal
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 128 })]);

      const steps = await drainOptimize(lm.optimizeLeaves(0));

      expect(steps).toHaveLength(0);
    });

    it("executes swaps and yields progress", async () => {
      // 8 x 16-sat leaves → optimize(multiplicity=0) produces one swap → [128]
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      const requestLeavesSwapMock = jest.fn(async (params: any) => {
        await params.onSwapInitiated?.();
        return params.targetAmounts.map((v: number, i: number) =>
          createMockTreeNode({ id: `new-${i}`, value: v }),
        );
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });
      await lm.addLeaves(leaves);

      const steps = await drainOptimize(lm.optimizeLeaves(0));

      // step 0 is yielded before any swap, then step 1..N after each swap
      expect(steps.length).toBeGreaterThanOrEqual(1);
      expect(steps[0]!.step).toBe(0);
      // Last step should have step === total
      expect(steps[steps.length - 1]!.step).toBe(
        steps[steps.length - 1]!.total,
      );
      // Swap service should have been called
      expect(requestLeavesSwapMock).toHaveBeenCalled();
      // Old leaves should be deleted (SPENT removes from cache), new leaves AVAILABLE
      for (const leaf of leaves) {
        expect(lm.getLeafRecordPublic(leaf.id)).toBeUndefined();
      }
      expect(lm.getAvailableBalance()).toBeGreaterThan(0);
    });

    it("locks all swap batches before releasing mutex", async () => {
      // 8 x 16 → optimize produces a single swap batch
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      const requestLeavesSwapMock = jest.fn(async (params: any) => {
        await params.onSwapInitiated?.();
        return [createMockTreeNode({ id: "new-0", value: 128 })];
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });
      await lm.addLeaves(leaves);

      // When requestLeavesSwap is called, all leaves should already be locked
      requestLeavesSwapMock.mockImplementation(async (params: any) => {
        // All original leaves should be LOCAL_LOCKED or SWAP_PENDING at this point
        for (const leaf of leaves) {
          const record = lm.getLeafRecordPublic(leaf.id);
          expect(["LOCAL_LOCKED", "SWAP_PENDING"]).toContain(record?.status);
        }
        await params.onSwapInitiated?.();
        return [createMockTreeNode({ id: "new-0", value: 128 })];
      });

      await drainOptimize(lm.optimizeLeaves(0));
    });

    it("restores LOCAL_LOCKED leaves and remaining batches on swap failure", async () => {
      // Use multiplicity=0: 8 x 16 = 128 → single swap [128]
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            // Fail before onSwapInitiated
            throw new Error("swap down");
          }),
        },
      });
      await lm.addLeaves(leaves);

      await expect(drainOptimize(lm.optimizeLeaves(0))).rejects.toThrow(
        "swap down",
      );

      // All leaves should be restored to AVAILABLE
      for (const leaf of leaves) {
        expect(lm.getLeafRecordPublic(leaf.id)?.status).toBe("AVAILABLE");
      }
    });

    it("preserves SWAP_PENDING on failure after onSwapInitiated", async () => {
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async (params: any) => {
            await params.onSwapInitiated?.();
            throw new Error("swap failed after initiation");
          }),
        },
      });
      await lm.addLeaves(leaves);

      await expect(drainOptimize(lm.optimizeLeaves(0))).rejects.toThrow(
        "swap failed after initiation",
      );

      // Leaves advanced to SWAP_PENDING should stay there
      for (const leaf of leaves) {
        expect(lm.getLeafRecordPublic(leaf.id)?.status).toBe("SWAP_PENDING");
      }
    });

    it("abort controller stops processing and restores unprocessed batches", async () => {
      // Use multiplicity=1 with a [64] leaf to produce a swap with multiple output leaves
      // so we can verify the abort behavior.
      // Actually, let's use a simpler setup: 2 leaves that require 2 separate swaps
      // by using multiplicity=1 with leaves [8, 64].
      // optimize([8, 64], 1) → [Swap([64], [2, 2, 4, 8, 16, 32])]
      // That's a single swap, which won't test abort well.
      // Let's instead test abort at the generator level directly.

      let swapCallCount = 0;
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async (params: any) => {
            swapCallCount++;
            await params.onSwapInitiated?.();
            return [
              createMockTreeNode({ id: `result-${swapCallCount}`, value: 128 }),
            ];
          }),
        },
      });

      // maximizeUnilateralExit with maxLeavesPerSwap=2 would produce multiple batches
      // but we can't control maxLeavesPerSwap. Instead, test that abort on step 0
      // prevents all swaps from executing.
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );
      await lm.addLeaves(leaves);

      const gen = lm.optimizeLeaves(0);
      const first = await gen.next();
      expect(first.done).toBe(false);
      // Abort after step 0 (before any swap executes)
      first.value!.controller.abort();

      // Drain remaining yields
      const steps: number[] = [];
      for await (const { step } of gen) {
        steps.push(step);
      }

      // All leaves should be restored to AVAILABLE (abort before swap)
      for (const leaf of leaves) {
        const record = lm.getLeafRecordPublic(leaf.id);
        expect(record?.status).toBe("AVAILABLE");
      }
    });

    it("prevents concurrent optimization (reentrancy guard)", async () => {
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      let resolveSwap: ((v: TreeNode[]) => void) | undefined;
      const requestLeavesSwapMock = jest.fn(
        (params: any) =>
          new Promise<TreeNode[]>(async (resolve) => {
            await params.onSwapInitiated?.();
            resolveSwap = resolve;
          }),
      );

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });
      await lm.addLeaves(leaves);

      // Start first optimization (will block on swap)
      const gen1Promise = drainOptimize(lm.optimizeLeaves(0));

      // Yield to let gen1 start (it will block in the swap)
      await new Promise((r) => setTimeout(r, 10));

      // Second optimization should be a no-op (reentrancy guard)
      const gen2Steps = await drainOptimize(lm.optimizeLeaves(0));
      expect(gen2Steps).toHaveLength(0);

      // Resolve the first swap
      resolveSwap!([createMockTreeNode({ id: "new-0", value: 128 })]);
      await gen1Promise;
    });

    it("clears optimizationInProgress flag even on error", async () => {
      const swapMock: jest.Mock = jest.fn(async () => {
        throw new Error("fail");
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: swapMock },
      });
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );
      await lm.addLeaves(leaves);

      await expect(drainOptimize(lm.optimizeLeaves(0))).rejects.toThrow("fail");

      // Replace mock with one that succeeds — verify reentrancy guard is cleared
      swapMock.mockImplementation(async (params: any) => {
        await params.onSwapInitiated?.();
        return params.targetAmounts.map((v: number, i: number) =>
          createMockTreeNode({ id: `retry-${i}`, value: v }),
        );
      });

      // Second call should succeed (flag was cleared in finally block)
      const gen2Steps = await drainOptimize(lm.optimizeLeaves(0));
      expect(gen2Steps.length).toBeGreaterThan(0);
    });
  });

  describe("autoOptimizeIfNeeded", () => {
    it("triggers optimization when auto=true and shouldOptimize returns true", async () => {
      const requestLeavesSwapMock = jest.fn(async (params: any) => {
        await params.onSwapInitiated?.();
        return params.targetAmounts.map((v: number, i: number) =>
          createMockTreeNode({ id: `opt-${i}`, value: v }),
        );
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
          getCoordinatorAddress: () => "mock-addr",
          getNetworkProto: () => 0,
        } as any,
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });

      // checkRenewLeaves is called by registerClaimedLeaves — mock it
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      // 8 x 16 triggers shouldOptimize for multiplicity=0
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );
      await lm.registerClaimedLeaves(leaves);

      // autoOptimizeIfNeeded is fire-and-forget — yield to let it complete
      await new Promise((r) => setTimeout(r, 100));

      expect(requestLeavesSwapMock).toHaveBeenCalled();
    });

    it("does not trigger when auto=false", async () => {
      const requestLeavesSwapMock = jest.fn(async () => []);

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );
      await lm.registerClaimedLeaves(leaves);

      expect(requestLeavesSwapMock).not.toHaveBeenCalled();
    });

    it("does not trigger when leaves are already optimal", async () => {
      const requestLeavesSwapMock = jest.fn(async () => []);

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      // Single power-of-two leaf — shouldOptimize returns false
      await lm.registerClaimedLeaves([
        createMockTreeNode({ id: "a", value: 128 }),
      ]);

      expect(requestLeavesSwapMock).not.toHaveBeenCalled();
    });

    it("swallows optimization errors silently", async () => {
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            throw new Error("swap service unavailable");
          }),
        },
      });
      (lm as any).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => nodes,
      );

      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      // Should not throw despite swap failure
      const result = await lm.registerClaimedLeaves(leaves);

      // autoOptimizeIfNeeded is fire-and-forget — yield to let it run
      await new Promise((r) => setTimeout(r, 50));

      expect(result).toHaveLength(8);

      // Leaves should be restored to AVAILABLE (not stuck in LOCAL_LOCKED)
      for (const leaf of leaves) {
        expect(lm.getLeafRecordPublic(leaf.id)?.status).toBe("AVAILABLE");
      }
    });
  });
});
