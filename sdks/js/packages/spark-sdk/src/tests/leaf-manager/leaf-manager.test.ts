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
});
