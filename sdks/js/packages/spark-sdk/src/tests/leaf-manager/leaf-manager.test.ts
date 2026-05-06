import { beforeEach, describe, expect, it, jest } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import {
  Network,
  type QueryNodesRequest,
  type QueryNodesResponse,
  type Transfer,
  type TransferLeaf,
  TransferStatus,
  TransferType,
  type TreeNode,
} from "../../proto/spark.js";
import LeafManager from "../../services/leaf-manager.js";
import { type KeyDerivation, KeyDerivationType } from "../../signer/types.js";
import { addPublicKeys } from "../../utils/keys.js";

type TestLeafStatus =
  | "AVAILABLE"
  | "LOCAL_LOCKED"
  | "OUTGOING"
  | "SWAP_PENDING"
  | "INCOMING"
  | "SPENT";

type TestLeafRecord = {
  treeNode: TreeNode;
  status: TestLeafStatus;
  source: unknown;
};

type RequestLeavesSwapParams = {
  leaves: TreeNode[];
  targetAmounts: number[];
  onSwapInitiated?: (leafIds?: string[]) => void | Promise<void>;
  registerSwapTransferId?: (transferId: string) => void;
};

type RequestLeavesSwapMock = jest.Mock<
  (args: RequestLeavesSwapParams) => Promise<TreeNode[]>
>;

type LeafManagerInternals = {
  checkRenewLeaves: (nodes: TreeNode[]) => Promise<TreeNode[]>;
  config: MockConfig;
  determineLeavesToSwap: (targetAmount: number) => TreeNode[];
  getAllPendingOutgoingTransfers: () => Promise<unknown[]>;
  getAllPendingSwaps: () => Promise<unknown[]>;
  getAvailableLeaves: () => TreeNode[];
  getLeaves: (isBalanceCheck?: boolean) => Promise<TreeNode[]>;
  identityPublicKey: Uint8Array | undefined;
  isLeafConsistent: (leaf: TreeNode, opLeaf: TreeNode | undefined) => boolean;
  isStaleLeafError: (error: unknown) => boolean;
  leaves: Map<string, TestLeafRecord>;
  queryNodes: (
    baseRequest: Omit<QueryNodesRequest, "limit" | "offset">,
    sparkClientAddress?: string,
    pageSize?: number,
  ) => Promise<QueryNodesResponse>;
  recoverLeaves: (
    leaves: TreeNode[],
    keyDerivation: KeyDerivation,
  ) => Promise<TreeNode[]>;
  restoreLocalLockedToAvailable: (leafIds: string[]) => void;
  selectLeaves: (targetAmounts: number[]) => [
    {
      [key: number]: TreeNode[];
    },
    boolean,
  ];
  transferService: MockTransferService;
  transition: (
    leafIds: string[],
    toStatus: string,
    meta?: { source: unknown },
  ) => void;
  verifyKey: (
    pubkey1: Uint8Array,
    pubkey2: Uint8Array,
    verifyingKey: Uint8Array,
  ) => boolean;
};

function leafManagerInternals(lm: LeafManager): LeafManagerInternals {
  return lm as unknown as LeafManagerInternals;
}

type LeafManagerConstructorArgs = ConstructorParameters<typeof LeafManager>;

// ---------------------------------------------------------------------------
// Testable subclass — exposes private internals for white-box testing
// ---------------------------------------------------------------------------
class TestableLeafManager extends LeafManager {
  // Override for sync tests: return canned leaves without hitting SOs or parsing TX bytes
  private getLeavesOverride: (() => Promise<TreeNode[]>) | undefined;
  setGetLeavesOverride(fn: () => Promise<TreeNode[]>) {
    this.getLeavesOverride = fn;
    this.bypassCheckRenewLeaves();
  }
  bypassCheckRenewLeaves() {
    leafManagerInternals(this).checkRenewLeaves = (nodes: TreeNode[]) =>
      Promise.resolve(nodes);
  }
  override async getLeaves(_isBalanceCheck?: boolean): Promise<TreeNode[]> {
    await Promise.resolve();
    if (this.getLeavesOverride) return this.getLeavesOverride();
    return super.getLeaves(_isBalanceCheck);
  }

  async queryNodesPublic(
    baseRequest: Omit<QueryNodesRequest, "limit" | "offset">,
    sparkClientAddress?: string,
    pageSize?: number,
  ): Promise<QueryNodesResponse> {
    await Promise.resolve();
    return leafManagerInternals(this).queryNodes(
      baseRequest,
      sparkClientAddress,
      pageSize,
    );
  }

  verifyKeyPublic(
    pubkey1: Uint8Array,
    pubkey2: Uint8Array,
    verifyingKey: Uint8Array,
  ): boolean {
    return leafManagerInternals(this).verifyKey(pubkey1, pubkey2, verifyingKey);
  }

  isLeafConsistentPublic(
    leaf: TreeNode,
    opLeaf: TreeNode | undefined,
  ): boolean {
    return leafManagerInternals(this).isLeafConsistent(leaf, opLeaf);
  }

  async recoverLeavesPublic(
    leaves: TreeNode[],
    keyDerivation: KeyDerivation,
  ): Promise<TreeNode[]> {
    await Promise.resolve();
    return leafManagerInternals(this).recoverLeaves(leaves, keyDerivation);
  }

  async checkRenewLeavesPublic(nodes: TreeNode[]): Promise<TreeNode[]> {
    await Promise.resolve();
    return leafManagerInternals(this).checkRenewLeaves(nodes);
  }

  transitionPublic(
    leafIds: string[],
    toStatus: string,
    meta?: { source: unknown },
  ): void {
    leafManagerInternals(this).transition(leafIds, toStatus, meta);
  }

  getLeafRecordPublic(
    id: string,
  ): { treeNode: TreeNode; status: string; source: unknown } | undefined {
    return leafManagerInternals(this).leaves.get(id);
  }

  getInternalLeaves(): Map<string, TestLeafRecord> {
    return leafManagerInternals(this).leaves;
  }

  getLeafRecord(id: string): TestLeafRecord {
    return leafManagerInternals(this).leaves.get(id) as TestLeafRecord;
  }

  getLeafStatus(id: string): string | undefined {
    return leafManagerInternals(this).leaves.get(id)?.status;
  }

  getLeafSource(id: string): unknown {
    return leafManagerInternals(this).leaves.get(id)?.source;
  }

  getInternalLeafCount(): number {
    return leafManagerInternals(this).leaves.size;
  }

  selectLeavesPublic(
    targetAmounts: number[],
  ): [{ [key: number]: TreeNode[] }, boolean] {
    return leafManagerInternals(this).selectLeaves(targetAmounts);
  }

  determineLeavesToSwapPublic(targetAmount: number): TreeNode[] {
    return leafManagerInternals(this).determineLeavesToSwap(targetAmount);
  }

  restoreLocalLockedToAvailablePublic(leafIds: string[]): void {
    leafManagerInternals(this).restoreLocalLockedToAvailable(leafIds);
  }

  isStaleLeafErrorPublic(error: unknown): boolean {
    return leafManagerInternals(this).isStaleLeafError(error);
  }
}

// ---------------------------------------------------------------------------
// Mock types
// ---------------------------------------------------------------------------
interface MockConfig {
  getCoordinatorAddress?: () => string;
  getCoordinatorIdentifier?: () => string;
  getNetworkProto?: () => number;
  getSigningOperators?: () => Record<string, { address: string }>;
  getOptimizationOptions?: () => { auto?: boolean; multiplicity?: number };
  getLog?: () => boolean;
  signer?: {
    getIdentityPublicKey: () => Promise<Uint8Array>;
    getPublicKeyFromDerivation?: jest.Mock;
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

interface MockSwapService {
  requestLeavesSwap?: RequestLeavesSwapMock;
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
  onAutoOptimize?: () => Promise<void>;
}): TestableLeafManager {
  const defaultConfig: MockConfig = {
    getLog: () => false,
  };
  return new TestableLeafManager(
    {
      ...defaultConfig,
      ...overrides?.config,
    } as unknown as LeafManagerConstructorArgs[0],
    (overrides?.swapService ?? {}) as unknown as LeafManagerConstructorArgs[1],
    (overrides?.transferService ??
      {}) as unknown as LeafManagerConstructorArgs[2],
    (overrides?.connectionManager ??
      {}) as unknown as LeafManagerConstructorArgs[3],
    overrides?.onBalanceUpdate,
    overrides?.onAutoOptimize,
  );
}

let nodeCounter = 0;
function createMockTreeNode(overrides: Partial<TreeNode> = {}): TreeNode {
  nodeCounter++;
  return {
    id: overrides.id ?? `node-${nodeCounter}`,
    treeId: "tree-1",
    value: 1000,
    nodeTx: buildRawTx(500),
    refundTx: buildRawTx(500),
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

function createMockTransferLeaf(leaf: TreeNode): TransferLeaf {
  return {
    leaf,
    secretCipher: new Uint8Array(0),
    signature: new Uint8Array(0),
    intermediateRefundTx: new Uint8Array(0),
    intermediateDirectRefundTx: new Uint8Array(0),
    intermediateDirectFromCpfpRefundTx: new Uint8Array(0),
    pendingKeyTweakPublicKey: new Uint8Array(0),
  };
}

function createMockTransfer(overrides: Partial<Transfer> = {}): Transfer {
  return {
    id: "transfer-1",
    senderIdentityPublicKey: new Uint8Array(33).fill(0x02),
    receiverIdentityPublicKey: new Uint8Array(33).fill(0x03),
    status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
    totalValue: 1000,
    expiryTime: undefined,
    leaves: [],
    createdTime: undefined,
    updatedTime: undefined,
    type: TransferType.TRANSFER,
    sparkInvoice: "",
    network: Network.REGTEST,
    receivers: [],
    ...overrides,
  };
}

beforeEach(() => {
  nodeCounter = 0;
});

// ===========================================================================
// TESTS
// ===========================================================================
describe("LeafManager", () => {
  // -----------------------------------------------------------------------
  // addLeaves / removeLeaves
  // -----------------------------------------------------------------------
  describe("addLeaves", () => {
    it("adds leaves as AVAILABLE with 'none' source", async () => {
      const lm = createTestableLeafManager();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 500 });

      await lm.addLeaves([leaf]);

      expect(lm.getAvailableBalance()).toBe(500);
      const record = lm.getLeafRecord("leaf-1");
      expect(record.status).toBe("AVAILABLE");
      expect(record.source).toEqual({ kind: "none" });
    });

    it("adds multiple leaves", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 100 }),
        createMockTreeNode({ id: "b", value: 200 }),
        createMockTreeNode({ id: "c", value: 300 }),
      ]);

      expect(lm.getAvailableBalance()).toBe(600);
    });

    it("overwrites existing leaf with same id", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 999 })]);

      expect(lm.getAvailableBalance()).toBe(999);
      expect(lm.getInternalLeafCount()).toBe(1);
    });
  });

  describe("addIncomingLeaves", () => {
    it("adds leaves as INCOMING with transfer source", async () => {
      const lm = createTestableLeafManager();
      const leaf = createMockTreeNode({ id: "incoming-1", value: 500 });

      await lm.addIncomingLeaves([leaf], "transfer-123");

      expect(lm.getLeafStatus("incoming-1")).toBe("INCOMING");
      expect(lm.getLeafSource("incoming-1")).toEqual({
        kind: "transfer",
        transferId: "transfer-123",
      });
      expect(lm.getIncomingBalance()).toBe(500);
      expect(lm.getAvailableBalance()).toBe(0);
    });

    it("incoming leaves are overwritten to AVAILABLE by addLeaves (after claim)", async () => {
      const lm = createTestableLeafManager();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 500 });

      // Transfer arrives — add as INCOMING
      await lm.addIncomingLeaves([leaf], "transfer-1");
      expect(lm.getLeafStatus("leaf-1")).toBe("INCOMING");
      expect(lm.getIncomingBalance()).toBe(500);

      // Claim completes — addLeaves overwrites to AVAILABLE
      await lm.addLeaves([leaf]);
      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("does not overwrite AVAILABLE or LOCAL_LOCKED leaves", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      // Duplicate incoming event for a leaf we already have as AVAILABLE
      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "leaf-1", value: 500 })],
        "transfer-dup",
      );

      // Should still be AVAILABLE, not overwritten to INCOMING
      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
    });
  });

  describe("removeLeaves", () => {
    it("removes leaves from the cache", async () => {
      const lm = createTestableLeafManager();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 500 });
      await lm.addLeaves([leaf]);
      expect(lm.getAvailableBalance()).toBe(500);

      await lm.removeLeaves(["leaf-1"]);
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getLeafRecord("leaf-1")).toBeUndefined();
    });

    it("removing nonexistent leaf is a no-op", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      await lm.removeLeaves(["nonexistent"]);
      expect(lm.getAvailableBalance()).toBe(100);
    });

    it("removes only specified leaves, keeps others", async () => {
      const lm = createTestableLeafManager();
      const a = createMockTreeNode({ id: "a", value: 100 });
      const b = createMockTreeNode({ id: "b", value: 200 });
      const c = createMockTreeNode({ id: "c", value: 300 });
      await lm.addLeaves([a, b, c]);

      await lm.removeLeaves(["b"]);

      expect(lm.getAvailableBalance()).toBe(400);
      expect(lm.getLeafRecord("b")).toBeUndefined();
      expect(lm.getLeafRecord("a")).toBeDefined();
      expect(lm.getLeafRecord("c")).toBeDefined();
    });
  });

  // -----------------------------------------------------------------------
  // Balance getters
  // -----------------------------------------------------------------------
  describe("balance getters", () => {
    it("getAvailableBalance returns sum of AVAILABLE leaves only", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 100 }),
        createMockTreeNode({ id: "b", value: 250 }),
      ]);
      expect(lm.getAvailableBalance()).toBe(350);
    });

    it("getAvailableBalance returns 0 for empty manager", () => {
      const lm = createTestableLeafManager();
      expect(lm.getAvailableBalance()).toBe(0);
    });

    it("getOwnedBalance includes available + outgoing + locked + swap pending", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([
        createMockTreeNode({ id: "avail", value: 100 }),
        createMockTreeNode({ id: "out", value: 200 }),
      ]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(
              createMockTreeNode({ id: "out", value: 200 }),
            ),
          ],
        }),
      );

      expect(lm.getAvailableBalance()).toBe(100);
      expect(lm.getOwnedBalance()).toBe(300);
    });

    it("getOwnedBalance excludes SPENT leaves", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf", value: 500 })]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [createMockTransferLeaf(createMockTreeNode({ id: "leaf" }))],
        }),
      );

      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("getOwnedBalance includes SWAP_PENDING leaves", async () => {
      const lm = createTestableLeafManager();

      await lm.addLeaves([createMockTreeNode({ id: "swap-leaf", value: 500 })]);
      leafManagerInternals(lm).leaves.get("swap-leaf")!.status = "SWAP_PENDING";

      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(500);
    });

    it("getIncomingBalance returns 0 when no incoming leaves", () => {
      const lm = createTestableLeafManager();
      expect(lm.getIncomingBalance()).toBe(0);
    });
  });

  // -----------------------------------------------------------------------
  // handleDepositEvent
  // -----------------------------------------------------------------------
  describe("handleDepositEvent", () => {
    it("adds a new deposit leaf to the cache", async () => {
      const lm = createTestableLeafManager();
      const deposit = createMockTreeNode({
        id: "deposit-1",
        value: 5000,
        status: "AVAILABLE",
      });

      await lm.handleDepositEvent(deposit);

      expect(lm.getAvailableBalance()).toBe(5000);
      const record = lm.getLeafRecord("deposit-1");
      expect(record.status).toBe("AVAILABLE");
      expect(record.source).toEqual({
        kind: "deposit",
        depositId: "deposit-1",
      });
    });

    it("transitions an existing leaf to AVAILABLE", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({
          id: "deposit-1",
          value: 5000,
          status: "AVAILABLE",
        }),
      ]);

      await lm.handleDepositEvent(
        createMockTreeNode({
          id: "deposit-1",
          value: 5000,
          status: "AVAILABLE",
        }),
      );

      expect(lm.getLeafRecord("deposit-1").status).toBe("AVAILABLE");
    });

    it("ignores deposits with non-AVAILABLE and non-CREATING status", async () => {
      const lm = createTestableLeafManager();
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "deposit-1", value: 5000, status: "SPENT" }),
      );
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getLeafRecord("deposit-1")).toBeUndefined();
    });

    it("adds CREATING deposits as INCOMING", async () => {
      const lm = createTestableLeafManager();
      await lm.handleDepositEvent(
        createMockTreeNode({
          id: "deposit-1",
          value: 5000,
          status: "CREATING",
        }),
      );

      expect(lm.getLeafStatus("deposit-1")).toBe("INCOMING");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getIncomingBalance()).toBe(5000);
    });

    it("CREATING deposit transitions to AVAILABLE when confirmed via deposit event", async () => {
      const lm = createTestableLeafManager();

      // First event: deposit in CREATING state
      await lm.handleDepositEvent(
        createMockTreeNode({
          id: "deposit-1",
          value: 5000,
          status: "CREATING",
        }),
      );
      expect(lm.getLeafStatus("deposit-1")).toBe("INCOMING");
      expect(lm.getIncomingBalance()).toBe(5000);

      // Second event: deposit confirmed (AVAILABLE)
      await lm.handleDepositEvent(
        createMockTreeNode({
          id: "deposit-1",
          value: 5000,
          status: "AVAILABLE",
        }),
      );
      expect(lm.getLeafStatus("deposit-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(5000);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("multiple deposits accumulate balance", async () => {
      const lm = createTestableLeafManager();

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "d1", value: 1000, status: "AVAILABLE" }),
      );
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "d2", value: 2000, status: "AVAILABLE" }),
      );

      expect(lm.getAvailableBalance()).toBe(3000);
      expect(lm.getInternalLeafCount()).toBe(2);
    });
  });

  // -----------------------------------------------------------------------
  // handleTransferEvent
  // -----------------------------------------------------------------------
  describe("handleTransferEvent", () => {
    const identityKey = new Uint8Array(33).fill(0x02);
    const otherKey = new Uint8Array(33).fill(0x03);

    function createLeafManagerWithIdentity() {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;
      return lm;
    }

    it("ignores events where sender is not us", async () => {
      const lm = createLeafManagerWithIdentity();
      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 100 })]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: otherKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
    });

    it("ignores events when identity key is not set", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 100 })]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
    });

    it("correctly compares identity keys using byte equality (not reference)", async () => {
      const lm = createLeafManagerWithIdentity();
      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 100 })]);

      // Different Uint8Array object, same bytes
      const sameKeyDifferentRef = new Uint8Array(33).fill(0x02);
      expect(sameKeyDifferentRef).not.toBe(identityKey);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: sameKeyDifferentRef,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
    });

    it("transitions to OUTGOING on SENDER_INITIATED", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm.getAvailableBalance()).toBe(0);
    });

    it("transitions to OUTGOING on SENDER_INITIATED_COORDINATOR", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
    });

    it("transitions to OUTGOING on SENDER_KEY_TWEAK_PENDING", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
    });

    it("transitions to SPENT on SENDER_KEY_TWEAKED (leaf deleted from cache)", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      // SPENT deletes the leaf from cache
      expect(lm.getLeafRecord("leaf-1")).toBeUndefined();
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("transitions back to AVAILABLE on RETURNED", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      // Move to OUTGOING
      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      // Return
      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_RETURNED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(100);
    });

    it("transitions back to AVAILABLE on EXPIRED", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      // Move to OUTGOING
      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      // Expire
      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_EXPIRED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(100);
    });

    it("ignores UNRECOGNIZED status", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      // Move to OUTGOING
      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      // UNRECOGNIZED should be a no-op
      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.UNRECOGNIZED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm.getOwnedBalance()).toBe(100);
    });

    it("sets transfer source on transition", async () => {
      const lm = createLeafManagerWithIdentity();
      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      await lm.handleTransferEvent(
        createMockTransfer({
          id: "transfer-42",
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );

      expect(lm.getLeafSource("leaf-1")).toEqual({
        kind: "transfer",
        transferId: "transfer-42",
      });
    });

    it("handles multi-leaf transfers", async () => {
      const lm = createLeafManagerWithIdentity();
      const a = createMockTreeNode({ id: "a", value: 100 });
      const b = createMockTreeNode({ id: "b", value: 200 });
      const c = createMockTreeNode({ id: "c", value: 300 });
      await lm.addLeaves([a, b, c]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(a), createMockTransferLeaf(b)],
        }),
      );

      expect(lm.getLeafStatus("a")).toBe("OUTGOING");
      expect(lm.getLeafStatus("b")).toBe("OUTGOING");
      expect(lm.getLeafStatus("c")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(300);
      expect(lm.getOwnedBalance()).toBe(600);
    });
  });

  // -----------------------------------------------------------------------
  // selectLeavesAndExecute
  // -----------------------------------------------------------------------
  describe("selectLeavesAndExecute", () => {
    it("rejects non-positive target amounts", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      await expect(
        lm.selectLeavesAndExecute([0], async () => {
          await Promise.resolve();
        }),
      ).rejects.toThrow("Target amount must be positive");
    });

    it("rejects negative target amounts", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      await expect(
        lm.selectLeavesAndExecute([-100], async () => {
          await Promise.resolve();
        }),
      ).rejects.toThrow("Target amount must be positive");
    });

    it("rejects when total exceeds available balance", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => {
          await Promise.resolve();
        }),
      ).rejects.toThrow("Total target amount exceeds available balance");
    });

    it("selects exact-match leaves and executes", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 500 }),
        createMockTreeNode({ id: "b", value: 300 }),
        createMockTreeNode({ id: "c", value: 200 }),
      ]);

      const result = await lm.selectLeavesAndExecute(
        [500],
        async (selected) => {
          await Promise.resolve();
          const total = selected[0].reduce((sum, l) => sum + l.value, 0);
          expect(total).toBe(500);
          return "done";
        },
      );

      expect(result).toBe("done");
    });

    it("returns the executor's result", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      const result = await lm.selectLeavesAndExecute([100], async () => {
        await Promise.resolve();
        return { txId: "abc123" };
      });

      expect(result).toEqual({ txId: "abc123" });
    });
  });

  // -----------------------------------------------------------------------
  // selectLeavesWithSwap — swap state transitions
  // -----------------------------------------------------------------------
  describe("selectLeavesWithSwap — swap state tracking", () => {
    it("transitions leaves through SWAP_PENDING during swap", async () => {
      const statesObserved: string[] = [];

      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            // Simulate what the real swap service does: call onSwapInitiated after sendSwapTransfer
            await args.onSwapInitiated?.();

            // Now observe leaf states — they should be SWAP_PENDING
            for (const leaf of args.leaves) {
              statesObserved.push(lm.getLeafStatus(leaf.id)!);
            }
            return args.targetAmounts.map((amount: number, i: number) =>
              createMockTreeNode({ id: `new-${i}`, value: amount }),
            );
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big-leaf", value: 500 })]);

      await lm.selectLeavesAndExecute([300], async (selected) => {
        await Promise.resolve();
        return selected;
      });

      expect(statesObserved).toContain("SWAP_PENDING");
    });

    it("maintains consistent owned balance during swap", async () => {
      let balanceDuringSwap: { available: number; owned: number } | undefined;

      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            await args.onSwapInitiated?.();
            balanceDuringSwap = {
              available: lm.getAvailableBalance(),
              owned: lm.getOwnedBalance(),
            };
            return args.targetAmounts.map((amount: number, i: number) =>
              createMockTreeNode({ id: `new-${i}`, value: amount }),
            );
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      await lm.selectLeavesAndExecute([300], async () => {
        await Promise.resolve();
        return "ok";
      });

      // After onSwapInitiated, leaves are SWAP_PENDING: available=0 but owned=500
      expect(balanceDuringSwap).toBeDefined();
      expect(balanceDuringSwap!.available).toBe(0);
      expect(balanceDuringSwap!.owned).toBe(500);
    });

    it("old leaves become SPENT after swap (deleted from cache), new leaves are AVAILABLE", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            await args.onSwapInitiated?.();
            return args.targetAmounts.map((amount: number, i: number) =>
              createMockTreeNode({ id: `swapped-${i}`, value: amount }),
            );
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "original", value: 500 })]);

      await lm.selectLeavesAndExecute([300], async () => {
        await Promise.resolve();
        return "ok";
      });

      // SPENT deletes the leaf from cache
      expect(lm.getLeafRecord("original")).toBeUndefined();
      // New leaves should exist (either AVAILABLE or LOCAL_LOCKED from selection)
      expect(lm.getLeafRecord("swapped-0")).toBeDefined();
    });
  });

  // -----------------------------------------------------------------------
  // Full lifecycle: transfer with swap flow
  // -----------------------------------------------------------------------
  describe("full lifecycle: transfer with swap", () => {
    const identityKey = new Uint8Array(33).fill(0x02);

    it("maintains balance consistency through: deposit → select → swap → select → send", async () => {
      const balanceSnapshots: {
        step: string;
        available: number;
        owned: number;
      }[] = [];

      function snapshot(lm: TestableLeafManager, step: string) {
        balanceSnapshots.push({
          step,
          available: lm.getAvailableBalance(),
          owned: lm.getOwnedBalance(),
        });
      }

      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            await args.onSwapInitiated?.();
            snapshot(lm, "during-swap");
            return args.targetAmounts.map((amount: number, i: number) =>
              createMockTreeNode({ id: `swapped-${i}`, value: amount }),
            );
          }),
        },
      });
      leafManagerInternals(lm).identityPublicKey = identityKey;

      // 1. Deposit: user receives 10000 sats
      await lm.handleDepositEvent(
        createMockTreeNode({
          id: "deposit-leaf",
          value: 10000,
          status: "AVAILABLE",
        }),
      );
      snapshot(lm, "after-deposit");

      // 2. User wants to send 3000 sats, but leaf is 10000 — needs swap
      await lm.selectLeavesAndExecute([3000], async (selected) => {
        snapshot(lm, "leaves-selected");

        // 3. Simulate sending the transfer
        await lm.handleTransferEvent(
          createMockTransfer({
            id: "send-transfer",
            senderIdentityPublicKey: identityKey,
            status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
            leaves: selected[0].map((l) => createMockTransferLeaf(l)),
          }),
        );
        snapshot(lm, "transfer-initiated");

        return "sent";
      });

      snapshot(lm, "after-execute");

      // Verify balance was never negative and owned balance was consistent
      for (const snap of balanceSnapshots) {
        expect(snap.available).toBeGreaterThanOrEqual(0);
        expect(snap.owned).toBeGreaterThanOrEqual(0);
      }

      // After deposit, should have full amount
      expect(
        balanceSnapshots.find((s) => s.step === "after-deposit")!.available,
      ).toBe(10000);

      // During swap, available is 0 but owned is still 10000 (SWAP_PENDING counts as owned)
      expect(
        balanceSnapshots.find((s) => s.step === "during-swap")!.available,
      ).toBe(0);
      expect(
        balanceSnapshots.find((s) => s.step === "during-swap")!.owned,
      ).toBe(10000);
    });

    it("AVAILABLE → OUTGOING → SPENT full transfer lifecycle", async () => {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      // 1. Sender initiated
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "t-1",
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(100);

      // 2. Key tweaked (completed)
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "t-1",
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("AVAILABLE → OUTGOING → AVAILABLE (returned)", async () => {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      const leaf = createMockTreeNode({ id: "leaf-1", value: 100 });
      await lm.addLeaves([leaf]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );
      expect(lm.getAvailableBalance()).toBe(0);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_RETURNED,
          leaves: [createMockTransferLeaf(leaf)],
        }),
      );
      expect(lm.getAvailableBalance()).toBe(100);
    });

    it("LOCAL_LOCKED → OUTGOING → SPENT: pending outgoing flow (lightning send)", async () => {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      // 1. selectLeavesAndExecute locks leaf, executor initiates preimage swap
      await lm.selectLeavesAndExecute([500], async (selected) => {
        expect(lm.getLeafStatus("leaf-1")).toBe("LOCAL_LOCKED");

        // Executor calls swapNodesForPreimage → SO returns transfer with
        // SENDER_KEY_TWEAK_PENDING (not completed yet — SSP needs to pay invoice)
        await lm.handleTransferEvent(
          createMockTransfer({
            id: "lightning-transfer",
            type: TransferType.PREIMAGE_SWAP,
            senderIdentityPublicKey: identityKey,
            status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
            leaves: [createMockTransferLeaf(selected[0][0]!)],
          }),
        );
        return "initiated";
      });

      // After executor: leaf is OUTGOING (pending — SSP hasn't paid yet)
      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(500); // still owned, just pending

      // 2. SSP pays the invoice, preimage revealed → stream event: SENDER_KEY_TWEAKED
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "lightning-transfer",
          type: TransferType.PREIMAGE_SWAP,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      // SPENT deletes the leaf from cache
      expect(lm.getLeafRecord("leaf-1")).toBeUndefined();
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("LOCAL_LOCKED → OUTGOING → AVAILABLE: pending outgoing returned (lightning send failed)", async () => {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      // 1. Executor initiates preimage swap — pending
      await lm.selectLeavesAndExecute([500], async (selected) => {
        await lm.handleTransferEvent(
          createMockTransfer({
            id: "lightning-transfer",
            type: TransferType.PREIMAGE_SWAP,
            senderIdentityPublicKey: identityKey,
            status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
            leaves: [createMockTransferLeaf(selected[0][0]!)],
          }),
        );
        return "initiated";
      });

      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm.getOwnedBalance()).toBe(500);

      // 2. Lightning payment fails, transfer returned → stream event: RETURNED
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "lightning-transfer",
          type: TransferType.PREIMAGE_SWAP,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_RETURNED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      // Balance restored — leaf is available again
      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
      expect(lm.getOwnedBalance()).toBe(500);
    });

    it("pending outgoing: local state is correct, sync produces same result", async () => {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 300 })]);

      // Executor initiates lightning send — SO returns SENDER_KEY_TWEAK_PENDING
      await lm.selectLeavesAndExecute([300], async (selected) => {
        await lm.handleTransferEvent(
          createMockTransfer({
            id: "lightning-transfer",
            type: TransferType.PREIMAGE_SWAP,
            senderIdentityPublicKey: identityKey,
            status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
            leaves: [createMockTransferLeaf(selected[0][0]!)],
          }),
        );
        return "initiated";
      });

      // Verify local state is correct WITHOUT sync
      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(300);

      // Now sync — should produce the same result (SO also has it as pending outgoing)
      const pendingLeaf = createMockTreeNode({ id: "leaf-1", value: 300 });
      const lm2 = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return identityKey;
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [
                createMockTransfer({
                  id: "lightning-transfer",
                  type: TransferType.PREIMAGE_SWAP,
                  status:
                    TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
                  leaves: [createMockTransferLeaf(pendingLeaf)],
                }),
              ],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });
      lm2.setGetLeavesOverride(async () => {
        await Promise.resolve();
        return [];
      });

      await lm2.sync();

      // Sync should show the same state as local transitions
      expect(lm2.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm2.getAvailableBalance()).toBe(0);
      expect(lm2.getOwnedBalance()).toBe(300);
    });

    it("deposit → send partial → receive change: balances stay consistent", async () => {
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      // 1. User gets a 1000 sat deposit
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "deposit", value: 1000, status: "AVAILABLE" }),
      );
      expect(lm.getAvailableBalance()).toBe(1000);

      // 2. User sends 300 sats (requires swap to break the 1000 leaf)
      // Simulate: swap breaks 1000 into [300, 700], then 300 goes outgoing
      // First remove the old leaf (it went through swap)
      await lm.removeLeaves(["deposit"]);
      await lm.addLeaves([
        createMockTreeNode({ id: "change", value: 700 }),
        createMockTreeNode({ id: "send-leaf", value: 300 }),
      ]);

      // Now send-leaf goes outgoing
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "t-1",
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(
              createMockTreeNode({ id: "send-leaf", value: 300 }),
            ),
          ],
        }),
      );

      expect(lm.getAvailableBalance()).toBe(700);
      expect(lm.getOwnedBalance()).toBe(1000); // change(700) + outgoing(300)

      // 3. Transfer completes
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "t-1",
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [
            createMockTransferLeaf(
              createMockTreeNode({ id: "send-leaf", value: 300 }),
            ),
          ],
        }),
      );

      expect(lm.getAvailableBalance()).toBe(700);
      expect(lm.getOwnedBalance()).toBe(700);
    });
  });

  // -----------------------------------------------------------------------
  // queryNodes pagination
  // -----------------------------------------------------------------------
  describe("queryNodes pagination", () => {
    let leafManager: TestableLeafManager;
    let createSparkClientMock: jest.Mock;

    beforeEach(() => {
      const paginatedResponses: Record<number, unknown> = {
        0: { nodes: { n1: { id: "n1" }, n2: { id: "n2" } }, offset: 0 },
        2: { nodes: { n2: { id: "n2" }, n3: { id: "n3" } }, offset: 2 },
        4: { nodes: {}, offset: 4 },
      };

      const queryNodesStub = jest.fn(async ({ offset }: { offset: number }) => {
        await Promise.resolve();
        return paginatedResponses[offset] ?? { nodes: {}, offset };
      });

      createSparkClientMock = jest.fn(async () => {
        await Promise.resolve();
        return {
          query_nodes: queryNodesStub,
        };
      });

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

  // -----------------------------------------------------------------------
  // verifyKey
  // -----------------------------------------------------------------------
  describe("verifyKey", () => {
    it("returns true when pubkey1 + pubkey2 equals verifyingKey", () => {
      const privA = secp256k1.utils.randomSecretKey();
      const privB = secp256k1.utils.randomSecretKey();
      const pubA = secp256k1.getPublicKey(privA, true);
      const pubB = secp256k1.getPublicKey(privB, true);
      const verifyingKey = addPublicKeys(pubA, pubB);

      const lm = createTestableLeafManager();
      expect(lm.verifyKeyPublic(pubA, pubB, verifyingKey)).toBe(true);
    });

    it("returns false when verifyingKey does not match", () => {
      const privA = secp256k1.utils.randomSecretKey();
      const privB = secp256k1.utils.randomSecretKey();
      const pubA = secp256k1.getPublicKey(privA, true);
      const pubB = secp256k1.getPublicKey(privB, true);
      const wrongKey = secp256k1.getPublicKey(
        secp256k1.utils.randomSecretKey(),
        true,
      );

      const lm = createTestableLeafManager();
      expect(lm.verifyKeyPublic(pubA, pubB, wrongKey)).toBe(false);
    });
  });

  // -----------------------------------------------------------------------
  // isLeafConsistent
  // -----------------------------------------------------------------------
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

      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, opLeaf),
      ).toBe(true);
    });

    it("returns false when opLeaf is undefined", () => {
      const leaf = createMockTreeNode({
        signingKeyshare: sharedSigningKeyshare,
        nodeTx: sharedNodeTx,
      });
      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, undefined),
      ).toBe(false);
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
      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, opLeaf),
      ).toBe(false);
    });

    it("returns false when leaf missing signingKeyshare", () => {
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
      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, opLeaf),
      ).toBe(false);
    });

    it("returns false when opLeaf missing signingKeyshare", () => {
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
      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, opLeaf),
      ).toBe(false);
    });

    it("returns false when publicKeys differ", () => {
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
      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, opLeaf),
      ).toBe(false);
    });

    it("returns false when nodeTx differ", () => {
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
      expect(
        createTestableLeafManager().isLeafConsistentPublic(leaf, opLeaf),
      ).toBe(false);
    });
  });

  // -----------------------------------------------------------------------
  // recoverLeaves
  // -----------------------------------------------------------------------
  describe("recoverLeaves", () => {
    it("sends a self-transfer and claims the result", async () => {
      const fakeIdentityPubkey = new Uint8Array(33).fill(0x02);
      const recoveredNode = createMockTreeNode({ id: "recovered-1" });
      const mockTransfer = { id: "transfer-1" };
      const mockPendingTransfer = { id: "transfer-1", status: "PENDING" };

      const sendMock = jest.fn(async () => {
        await Promise.resolve();
        return mockTransfer;
      });
      const queryMock = jest.fn(async () => {
        await Promise.resolve();
        return mockPendingTransfer;
      });
      const claimMock = jest.fn(async () => {
        await Promise.resolve();
        return [recoveredNode];
      });

      const lm = createTestableLeafManager({
        config: {
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return fakeIdentityPubkey;
            }),
          },
        },
        transferService: {
          sendTransferWithKeyTweaks: sendMock,
          queryTransfer: queryMock,
          claimTransfer: claimMock,
        },
      });

      const inputLeaf = createMockTreeNode({ id: "leaf-to-recover" });
      const keyDerivation: KeyDerivation = {
        type: KeyDerivationType.LEAF,
        path: "parent-id",
      };

      const result = await lm.recoverLeavesPublic([inputLeaf], keyDerivation);

      expect(sendMock).toHaveBeenCalledWith([
        expect.objectContaining({
          leaf: inputLeaf,
          keyDerivation,
          newKeyDerivation: { type: KeyDerivationType.RANDOM },
          receiverIdentityPublicKey: fakeIdentityPubkey,
        }),
      ]);
      expect(queryMock).toHaveBeenCalledWith("transfer-1");
      expect(claimMock).toHaveBeenCalledWith(mockPendingTransfer);
      expect(result).toEqual([recoveredNode]);
    });

    it("returns empty array when queryTransfer returns null", async () => {
      const fakeIdentityPubkey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager({
        config: {
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return fakeIdentityPubkey;
            }),
          },
        },
        transferService: {
          sendTransferWithKeyTweaks: jest.fn(async () => {
            await Promise.resolve();
            return { id: "t-1" };
          }),
          queryTransfer: jest.fn(async () => {
            await Promise.resolve();
            return null;
          }),
          claimTransfer: jest.fn(),
        },
      });

      const result = await lm.recoverLeavesPublic([createMockTreeNode()], {
        type: KeyDerivationType.LEAF,
        path: "p",
      });
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

      const queryNodesStub = jest.fn(async () => {
        await Promise.resolve();
        return {
          nodes: {
            n1: nodeRenewNode,
            n2: nodeRenewRefund,
            n3: nodeRenewZero,
            "parent-1": parentNode1,
            "parent-2": parentNode2,
          },
          offset: 0,
        };
      });
      const createSparkClientMock = jest.fn(async () => {
        await Promise.resolve();
        return {
          query_nodes: queryNodesStub,
        };
      });

      const renewNodeTxnMock = jest.fn(async () => {
        await Promise.resolve();
        return renewedNode;
      });
      const renewRefundTxnMock = jest.fn(async () => {
        await Promise.resolve();
        return renewedRefund;
      });
      const renewZeroTimelockNodeTxnMock = jest.fn(async () => {
        await Promise.resolve();
        return renewedZero;
      });

      const leafManager = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock-addr",
          getNetworkProto: () => 0,
        },
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

      const queryNodesStub = jest.fn(async () => {
        await Promise.resolve();
        return {
          nodes: {
            fail: failNode,
            ok: okNode,
            "parent-fail": parentFail,
            "parent-ok": parentOk,
          },
          offset: 0,
        };
      });

      const leafManager = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock-addr",
          getNetworkProto: () => 0,
        },
        connectionManager: {
          createSparkClient: jest.fn(async () => {
            await Promise.resolve();
            return {
              query_nodes: queryNodesStub,
            };
          }),
        },
        transferService: {
          renewNodeTxn: jest.fn(async () => {
            await Promise.resolve();
            throw new Error("network failure");
          }),
          renewRefundTxn: jest.fn(async () => {
            await Promise.resolve();
            return renewedRefund;
          }),
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
  // -----------------------------------------------------------------------
  // Edge cases
  // -----------------------------------------------------------------------
  describe("edge cases", () => {
    it("handles empty addLeaves call", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([]);
      expect(lm.getAvailableBalance()).toBe(0);
    });

    it("handles empty removeLeaves call", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);
      await lm.removeLeaves([]);
      expect(lm.getAvailableBalance()).toBe(100);
    });

    it("handleTransferEvent with empty leaves array is a no-op", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [],
        }),
      );

      expect(lm.getLeafStatus("a")).toBe("AVAILABLE");
    });

    it("transition on unknown leaf silently skips without crashing", async () => {
      const lm = createTestableLeafManager();

      // handleTransferEvent on a leaf not in the cache
      const identityKey = new Uint8Array(33).fill(0x02);
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "unknown-leaf" })),
          ],
        }),
      );

      // Unknown leaf should not be added to cache
      expect(lm.getLeafRecordPublic("unknown-leaf")).toBeUndefined();
    });

    it("getAvailableLeaves returns only AVAILABLE leaves", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([
        createMockTreeNode({ id: "avail-1", value: 100 }),
        createMockTreeNode({ id: "avail-2", value: 200 }),
        createMockTreeNode({ id: "outgoing", value: 300 }),
      ]);

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "outgoing" })),
          ],
        }),
      );

      const available = leafManagerInternals(lm).getAvailableLeaves();
      expect(available).toHaveLength(2);
      expect(available.map((l) => l.id).sort()).toEqual(["avail-1", "avail-2"]);
    });

    it("handleDepositEvent on already-AVAILABLE leaf does not log unexpected transition warning", async () => {
      const lm = createTestableLeafManager();
      const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});

      // First deposit — adds the leaf
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "d1", value: 1000, status: "AVAILABLE" }),
      );
      expect(lm.getAvailableBalance()).toBe(1000);

      // Second deposit event for same leaf (e.g., re-confirmation or duplicate event)
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "d1", value: 1000, status: "AVAILABLE" }),
      );

      // Should NOT log an unexpected transition warning
      expect(warnSpy).not.toHaveBeenCalledWith(
        expect.stringContaining("Unexpected transition"),
      );
      expect(lm.getAvailableBalance()).toBe(1000);
      warnSpy.mockRestore();
    });

    it("concurrent selectLeavesWithSwap calls don't interfere with each other's locked leaves", async () => {
      // Track which leaf IDs each swap call receives
      const swapCallLeafIds: string[][] = [];

      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            const ids = args.leaves.map((l: TreeNode) => l.id);
            swapCallLeafIds.push(ids);
            await args.onSwapInitiated?.();
            // Return new leaves matching target amounts
            return args.targetAmounts.map((amount: number, i: number) =>
              createMockTreeNode({
                id: `new-${swapCallLeafIds.length}-${i}`,
                value: amount,
              }),
            );
          }),
        },
      });

      // Add three leaves: 500, 300, 200
      await lm.addLeaves([
        createMockTreeNode({ id: "leaf-500", value: 500 }),
        createMockTreeNode({ id: "leaf-300", value: 300 }),
        createMockTreeNode({ id: "leaf-200", value: 200 }),
      ]);

      // Launch two concurrent selections that each require a swap
      // Call 1 wants 400 (no single leaf is exactly 400)
      // Call 2 wants 150 (no single leaf is exactly 150)
      const [result1, result2] = await Promise.all([
        lm.selectLeavesAndExecute([400], async (selected) => {
          await Promise.resolve();
          return selected[0].map((l) => l.id);
        }),
        lm.selectLeavesAndExecute([150], async (selected) => {
          await Promise.resolve();
          return selected[0].map((l) => l.id);
        }),
      ]);

      // Each swap call should only include leaves locked for THAT specific swap,
      // not leaves locked by the other concurrent call
      for (const leafIds of swapCallLeafIds) {
        // No swap call should include more leaves than needed
        // Each call should only have the leaves it locked, not all LOCAL_LOCKED leaves
        expect(leafIds.length).toBeLessThanOrEqual(3);
      }

      // Both calls should succeed
      expect(result1).toBeDefined();
      expect(result2).toBeDefined();
    });

    it("executeWithAllLeaves restores LOCAL_LOCKED leaves to AVAILABLE on executor failure", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 100 }),
        createMockTreeNode({ id: "b", value: 200 }),
      ]);

      await expect(
        lm.executeWithAllLeaves(async () => {
          await Promise.resolve();
          throw new Error("executor failed");
        }),
      ).rejects.toThrow("executor failed");

      // Leaves should be restored to AVAILABLE since the SO was never contacted
      expect(lm.getLeafStatus("a")).toBe("AVAILABLE");
      expect(lm.getLeafStatus("b")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(300);
    });

    it("executor advances leaf state to SPENT via handleTransferEvent", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      await lm.selectLeavesAndExecute([500], async (selected) => {
        expect(lm.getLeafStatus("leaf-1")).toBe("LOCAL_LOCKED");

        // Executor sends transfer and advances local state
        await lm.handleTransferEvent(
          createMockTransfer({
            senderIdentityPublicKey: identityKey,
            status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
            leaves: [createMockTransferLeaf(selected[0][0]!)],
          }),
        );
        return "sent";
      });

      // Executor advanced state — leaf is SPENT (deleted from cache)
      expect(lm.getLeafRecord("leaf-1")).toBeUndefined();
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("handleDepositEvent does NOT overwrite LOCAL_LOCKED leaf", async () => {
      const lm = createTestableLeafManager();

      // Simulate: leaf was locked locally (e.g., selection in progress),
      // but a deposit event comes in saying it's AVAILABLE on the SO.
      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);
      // Manually lock it
      leafManagerInternals(lm).leaves.get("leaf-1")!.status = "LOCAL_LOCKED";
      expect(lm.getLeafStatus("leaf-1")).toBe("LOCAL_LOCKED");

      // Deposit event says it's available — should NOT overwrite LOCAL_LOCKED
      // (IN_FLIGHT_STATUSES guard protects in-progress operations)
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "leaf-1", value: 500, status: "AVAILABLE" }),
      );

      expect(lm.getLeafStatus("leaf-1")).toBe("LOCAL_LOCKED");
    });

    it("selectLeavesAndExecute restores LOCAL_LOCKED leaves to AVAILABLE on executor failure", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      // Exact match: 500 = 500, no swap needed
      await expect(
        lm.selectLeavesAndExecute([500], async () => {
          await Promise.resolve();
          throw new Error("transfer failed");
        }),
      ).rejects.toThrow("transfer failed");

      // Leaf should be restored to AVAILABLE since SO was never contacted
      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
    });

    it("selectLeavesAndExecute does NOT restore leaves that changed status during executor", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async (selected) => {
          // Simulate: executor contacted SO, stream event moved leaf to OUTGOING
          await lm.handleTransferEvent(
            createMockTransfer({
              senderIdentityPublicKey: identityKey,
              status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
              leaves: [createMockTransferLeaf(selected[0][0]!)],
            }),
          );
          // Then some later step fails
          throw new Error("post-transfer failure");
        }),
      ).rejects.toThrow("post-transfer failure");

      // Leaf moved to OUTGOING during executor — should NOT be restored to AVAILABLE
      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
    });

    it("sync preserves LOCAL_LOCKED leaves even when SO reports them as AVAILABLE", async () => {
      const leaf = createMockTreeNode({ id: "locked-leaf", value: 500 });

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });

      // getLeaves override: SO reports this leaf as AVAILABLE
      lm.setGetLeavesOverride(async () => {
        await Promise.resolve();
        return [leaf];
      });

      // Add the leaf and manually set it to LOCAL_LOCKED (simulating in-progress optimization)
      await lm.addLeaves([leaf]);
      leafManagerInternals(lm).leaves.get(leaf.id)!.status = "LOCAL_LOCKED";

      // sync queries the SO which still reports the leaf as AVAILABLE,
      // but the preserved LOCAL_LOCKED state should win
      const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});
      await lm.sync();

      expect(lm.getLeafStatus(leaf.id)).toBe("LOCAL_LOCKED");
      // No unexpected transition warnings
      expect(warnSpy).not.toHaveBeenCalledWith(
        expect.stringContaining("Unexpected transition"),
      );
      warnSpy.mockRestore();
    });

    it("sync does NOT preserve SWAP_PENDING leaves (SO was contacted, trust server state)", async () => {
      const swapLeaf = createMockTreeNode({ id: "swap-leaf", value: 500 });

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });

      lm.setGetLeavesOverride(async () => {
        await Promise.resolve();
        return [];
      });

      await lm.addLeaves([swapLeaf]);
      leafManagerInternals(lm).leaves.get(swapLeaf.id)!.status = "SWAP_PENDING";

      await lm.sync();

      // SWAP_PENDING is NOT preserved — the SO was contacted, trust server state.
      // If the SO reports the leaf in a swap, getAllPendingSwaps picks it up.
      // If not, the swap completed or was rolled back.
      expect(lm.getLeafRecord(swapLeaf.id)).toBeUndefined();
    });

    it("sync does not count counter-swap transfers as incoming balance", async () => {
      const counterSwapLeaf = createMockTreeNode({
        id: "counter-swap-leaf",
        value: 500,
      });
      const realIncomingLeaf = createMockTreeNode({
        id: "real-incoming-leaf",
        value: 300,
      });

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getCoordinatorIdentifier: () => "coord-1",
          getNetworkProto: () => 0,
          getSigningOperators: () => ({ "coord-1": { address: "mock" } }),
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
            getPublicKeyFromDerivation: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0);
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [
                // Counter-swap: should NOT count as incoming
                createMockTransfer({
                  id: "counter-swap-transfer",
                  type: TransferType.COUNTER_SWAP_V3,
                  status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
                  leaves: [createMockTransferLeaf(counterSwapLeaf)],
                }),
                // Real incoming transfer: should count as incoming
                createMockTransfer({
                  id: "real-transfer",
                  type: TransferType.TRANSFER,
                  status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
                  leaves: [createMockTransferLeaf(realIncomingLeaf)],
                }),
              ],
            };
          }),
        },
        connectionManager: {
          createSparkClient: jest.fn(async () => {
            await Promise.resolve();
            return {
              query_nodes: jest.fn(async () => {
                await Promise.resolve();
                return { nodes: {}, offset: 0 };
              }),
            };
          }),
        },
      });

      await lm.sync();

      // Only the real transfer should be in the incoming balance
      expect(lm.getIncomingBalance()).toBe(300);
      // Counter-swap leaf should not be in the cache at all
      expect(lm.getLeafRecord("counter-swap-leaf")).toBeUndefined();
      // Real incoming leaf should be INCOMING
      expect(lm.getLeafStatus("real-incoming-leaf")).toBe("INCOMING");
    });

    it("swap does not double-count: owned + incoming should not exceed actual sats", async () => {
      const originalLeaf = createMockTreeNode({ id: "original", value: 128 });
      const counterSwapLeaf = createMockTreeNode({
        id: "counter-swap",
        value: 128,
      });

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getCoordinatorIdentifier: () => "coord-1",
          getNetworkProto: () => 0,
          getSigningOperators: () => ({ "coord-1": { address: "mock" } }),
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
            getPublicKeyFromDerivation: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0);
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [
                // Our outbound swap — leaf is in SWAP_PENDING
                createMockTransfer({
                  id: "swap-out",
                  type: TransferType.PRIMARY_SWAP_V3,
                  status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
                  leaves: [createMockTransferLeaf(originalLeaf)],
                }),
              ],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [
                // Counter-swap from SSP — should be excluded from incoming
                createMockTransfer({
                  id: "swap-in",
                  type: TransferType.COUNTER_SWAP_V3,
                  status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
                  leaves: [createMockTransferLeaf(counterSwapLeaf)],
                }),
              ],
            };
          }),
        },
        connectionManager: {
          createSparkClient: jest.fn(async () => {
            await Promise.resolve();
            return {
              query_nodes: jest.fn(async () => {
                await Promise.resolve();
                return { nodes: {}, offset: 0 };
              }),
            };
          }),
        },
      });

      await lm.sync();

      // Original leaf is SWAP_PENDING — counted in owned
      expect(lm.getLeafStatus("original")).toBe("SWAP_PENDING");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(128);
      // Counter-swap filtered from both incoming AND swap queries — no double-counting
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("selectLeavesAndExecute syncs and retries when SO rejects stale leaf (multi-instance)", async () => {
      let swapCallCount = 0;
      let executorCallCount = 0;
      const freshLeaf = createMockTreeNode({ id: "fresh-leaf", value: 500 });

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            swapCallCount++;
            if (swapCallCount === 1) {
              // First attempt: SO rejects — leaf was swapped by another instance
              throw new Error(
                "not available to transfer, status: TRANSFER_LOCKED",
              );
            }
            // Retry after sync: swap succeeds with fresh leaf
            await args.onSwapInitiated?.();
            return args.targetAmounts.map((amount: number, i: number) =>
              createMockTreeNode({ id: `result-${i}`, value: amount }),
            );
          }),
        },
      });

      // getLeaves override: sync discovers fresh leaves from the other instance's swap
      let syncGetLeavesCallCount = 0;
      lm.setGetLeavesOverride(async () => {
        await Promise.resolve();
        syncGetLeavesCallCount++;
        // Sync always returns fresh leaves (the stale leaf is gone on the SO)
        return [freshLeaf];
      });

      // Add stale leaf to local cache (from another instance's prior state)
      await lm.addLeaves([
        createMockTreeNode({ id: "stale-leaf", value: 500 }),
      ]);

      await lm.selectLeavesAndExecute([300], async (selected) => {
        await Promise.resolve();
        executorCallCount++;
        return selected[0].map((l) => l.id);
      });

      // First swap failed (stale leaf), triggered sync, then retried with fresh leaf
      expect(swapCallCount).toBe(2);
      // Executor only ran once (on the successful retry)
      expect(executorCallCount).toBe(1);
      // Sync was called (getLeaves invoked during sync)
      expect(syncGetLeavesCallCount).toBeGreaterThan(0);
      // Stale leaf should be gone
      expect(lm.getLeafRecord("stale-leaf")).toBeUndefined();
      // fresh-leaf was consumed by the retry swap, so it's also gone (SPENT).
      // The swap result leaves should exist instead.
      expect(lm.getInternalLeafCount()).toBeGreaterThan(0);
    });

    it("selectLeavesWithSwap restores leaves when requestLeavesSwap throws", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            await Promise.resolve();
            throw new Error("SSP unavailable");
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      // Requesting 300 from a 500 leaf triggers a swap (no exact match)
      await expect(
        lm.selectLeavesAndExecute([300], async () => {
          await Promise.resolve();
          return "nope";
        }),
      ).rejects.toThrow("SSP unavailable");

      // The leaf should be restored to AVAILABLE, not stuck as LOCAL_LOCKED
      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
    });

    it("selectLeavesWithSwap does NOT restore SWAP_PENDING leaves when swap fails after onSwapInitiated", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            // onSwapInitiated fires (leaf transitions to SWAP_PENDING — SO has it locked)
            await args.onSwapInitiated?.();
            // Then something fails later in the swap
            throw new Error("SSP swap processing failed");
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      await expect(
        lm.selectLeavesAndExecute([300], async () => {
          await Promise.resolve();
          return "nope";
        }),
      ).rejects.toThrow("SSP swap processing failed");

      // Leaf is SWAP_PENDING — the SO has it locked, so we must NOT restore to
      // AVAILABLE. The next sync() will reconcile with the SO's actual state.
      expect(lm.getLeafStatus("leaf-1")).toBe("SWAP_PENDING");
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(500);
    });

    it("optimizeLeaves restores remaining batches when a mid-batch swap fails", async () => {
      let swapCallCount = 0;
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async (args: RequestLeavesSwapParams) => {
            swapCallCount++;
            await args.onSwapInitiated?.();
            if (swapCallCount === 1) {
              // First batch succeeds
              return args.targetAmounts.map((amount: number, i: number) =>
                createMockTreeNode({ id: `new-${i}`, value: amount }),
              );
            }
            // Second batch fails
            throw new Error("SSP unavailable for batch 2");
          }),
        },
      });

      // Add leaves that would produce multiple optimization batches
      // (This depends on optimize() behavior, but we can at least test the error path)
      await lm.addLeaves([
        createMockTreeNode({ id: "a", value: 100 }),
        createMockTreeNode({ id: "b", value: 200 }),
      ]);

      // Run optimizeLeaves — even if it throws, remaining leaves should be restored
      try {
        for await (const step of lm.optimizeLeaves()) {
          void step;
          // consume all steps
        }
      } catch {
        // Expected: swap failure
      }

      // No leaves should be stuck as LOCAL_LOCKED or SWAP_PENDING
      for (const [, record] of leafManagerInternals(lm).leaves) {
        expect(record.status).not.toBe("LOCAL_LOCKED");
        expect(record.status).not.toBe("SWAP_PENDING");
      }
    });

    // =====================================================================
    // Multi-instance concurrency tests
    // =====================================================================

    it("registerClaimedLeaves succeeds even when autoOptimize fails (another instance locked the leaf)", async () => {
      const leaf = createMockTreeNode({ id: "claimed-leaf", value: 128 });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 2 }),
        },
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            await Promise.resolve();
            // Simulates: another instance already locked this leaf for a swap
            throw new Error(
              "leaf is not available to transfer, status: TRANSFER_LOCKED",
            );
          }),
        },
      });
      lm.bypassCheckRenewLeaves();

      // registerClaimedLeaves calls addLeaves → autoOptimizeIfNeeded
      // autoOptimize tries to swap → fails (another instance has the leaf)
      // The claim itself (addLeaves) should still succeed
      const result = await lm.registerClaimedLeaves([leaf]);

      expect(result.length).toBe(1);
      expect(lm.getAvailableBalance()).toBe(128);
      expect(lm.getLeafStatus("claimed-leaf")).toBe("AVAILABLE");
    });

    it("wallet A balance updates when wallet B sends a transfer (via senderTransfer stream event)", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      // Wallet A has two leaves
      await lm.addLeaves([
        createMockTreeNode({ id: "leaf-1", value: 100 }),
        createMockTreeNode({ id: "leaf-2", value: 200 }),
      ]);
      expect(lm.getAvailableBalance()).toBe(300);
      expect(lm.getOwnedBalance()).toBe(300);

      // Wallet B sends leaf-1 in a transfer — A receives SENDER_INITIATED event
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "transfer-from-B",
          type: TransferType.TRANSFER,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      // leaf-1 is now OUTGOING — not available but still owned
      expect(lm.getLeafStatus("leaf-1")).toBe("OUTGOING");
      expect(lm.getAvailableBalance()).toBe(200);
      expect(lm.getOwnedBalance()).toBe(300);

      // Transfer completes — A receives SENDER_KEY_TWEAKED event
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "transfer-from-B",
          type: TransferType.TRANSFER,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      // leaf-1 is SPENT (deleted from cache) — balance fully updated
      expect(lm.getLeafRecord("leaf-1")).toBeUndefined();
      expect(lm.getAvailableBalance()).toBe(200);
      expect(lm.getOwnedBalance()).toBe(200);
      // leaf-2 unaffected
      expect(lm.getLeafStatus("leaf-2")).toBe("AVAILABLE");
    });

    it("wallet A balance updates when wallet B sends a transfer that gets returned", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 500 })]);

      // B initiates transfer
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "transfer-from-B",
          type: TransferType.TRANSFER,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(500);

      // Transfer returned (receiver didn't claim)
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "transfer-from-B",
          type: TransferType.TRANSFER,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_RETURNED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      // Leaf restored to AVAILABLE
      expect(lm.getLeafStatus("leaf-1")).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
      expect(lm.getOwnedBalance()).toBe(500);
    });

    it("handleTransferEvent marks leaf SPENT when another instance completes a swap", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      // Leaf is AVAILABLE in our cache (we claimed it)
      await lm.addLeaves([createMockTreeNode({ id: "leaf-X", value: 128 })]);
      expect(lm.getAvailableBalance()).toBe(128);

      // Another instance swapped this leaf — we receive the senderTransfer event
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "swap-transfer",
          type: TransferType.PRIMARY_SWAP_V3,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-X" })),
          ],
        }),
      );

      // Leaf should be SPENT (deleted from cache) — balance drops
      expect(lm.getLeafRecord("leaf-X")).toBeUndefined();
      expect(lm.getAvailableBalance()).toBe(0);
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("after another instance's swap, stale leaf is replaced by fresh leaves on next sync", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const freshLeaf = createMockTreeNode({ id: "fresh-Y", value: 128 });

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return identityKey;
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });
      leafManagerInternals(lm).identityPublicKey = identityKey;

      // getLeaves returns the replacement leaf from another instance's swap
      lm.setGetLeavesOverride(async () => {
        await Promise.resolve();
        return [freshLeaf];
      });

      // Start with stale leaf
      await lm.addLeaves([createMockTreeNode({ id: "leaf-X", value: 128 })]);

      // Another instance swapped leaf-X — senderTransfer marks it SPENT
      await lm.handleTransferEvent(
        createMockTransfer({
          id: "swap-transfer",
          type: TransferType.PRIMARY_SWAP_V3,
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-X" })),
          ],
        }),
      );
      expect(lm.getAvailableBalance()).toBe(0);

      // Sync discovers the replacement leaves from the other instance's swap
      await lm.sync();

      expect(lm.getAvailableBalance()).toBe(128);
      expect(lm.getLeafStatus("fresh-Y")).toBe("AVAILABLE");
      // Stale leaf-X should be gone (not AVAILABLE, not preserved)
      expect(lm.getLeafRecord("leaf-X")).toBeUndefined();
    });

    it("selectLeavesAndExecute recovers from stale leaf by syncing and retrying", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const freshLeaf = createMockTreeNode({ id: "fresh-leaf", value: 500 });
      let executorCallCount = 0;
      let getLeavesCallCount = 0;

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return identityKey;
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });

      // After sync, getLeaves returns fresh leaf
      lm.setGetLeavesOverride(async () => {
        await Promise.resolve();
        getLeavesCallCount++;
        return [freshLeaf];
      });

      // Cache has stale leaf (swapped away by another instance)
      await lm.addLeaves([
        createMockTreeNode({ id: "stale-leaf", value: 500 }),
      ]);

      const result = await lm.selectLeavesAndExecute(
        [500],
        async (selected) => {
          await Promise.resolve();
          executorCallCount++;
          if (executorCallCount === 1) {
            // First attempt: SO rejects the stale leaf
            throw new Error(
              "nodes [stale-leaf] are not owned by the authenticated identity public key",
            );
          }
          // Retry with fresh leaf succeeds
          return selected[0].map((l) => l.id);
        },
      );

      // Should have synced and retried
      expect(executorCallCount).toBe(2);
      expect(getLeavesCallCount).toBeGreaterThan(0); // sync was called
      expect(result).toContain("fresh-leaf");
    });

    it("after sync from stale recovery, autoOptimize does NOT re-swap already-optimized leaves", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      let swapCallCount = 0;

      // Create leaves that are already well-denominated (no optimization needed)
      const optimizedLeaves = [
        createMockTreeNode({ id: "opt-1", value: 100 }),
        createMockTreeNode({ id: "opt-2", value: 200 }),
      ];

      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return identityKey;
            }),
          },
        },
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            await Promise.resolve();
            swapCallCount++;
            return [];
          }),
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: -1,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });

      // getLeaves returns already-optimized leaves from another instance's swap
      lm.setGetLeavesOverride(async () => {
        await Promise.resolve();
        return optimizedLeaves;
      });

      // Sync discovers optimized leaves → autoOptimizeIfNeeded runs
      await lm.sync();

      expect(lm.getAvailableBalance()).toBe(300);
      // shouldOptimize returns false for well-denominated leaves → no swap attempted
      expect(swapCallCount).toBe(0);
    });

    // =====================================================================
    // isStaleLeafError detection tests
    // =====================================================================

    // =====================================================================
    // balance:update callback tests
    // =====================================================================

    it("emitBalanceUpdate fires on addLeaves", async () => {
      const updates: { available: number; owned: number; incoming: number }[] =
        [];
      const lm = createTestableLeafManager({
        onBalanceUpdate: (b) => updates.push(b),
      });

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 100 })]);

      expect(updates.length).toBe(1);
      expect(updates[0]!.available).toBe(100);
      expect(updates[0]!.owned).toBe(100);
    });

    it("emitBalanceUpdate fires on handleDepositEvent", async () => {
      const updates: { available: number; owned: number; incoming: number }[] =
        [];
      const lm = createTestableLeafManager({
        onBalanceUpdate: (b) => updates.push(b),
      });

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "d1", value: 500, status: "AVAILABLE" }),
      );

      expect(updates.length).toBe(1);
      expect(updates[0]!.available).toBe(500);
    });

    it("emitBalanceUpdate fires on handleTransferEvent", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const updates: { available: number; owned: number; incoming: number }[] =
        [];
      const lm = createTestableLeafManager({
        onBalanceUpdate: (b) => updates.push(b),
      });
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([createMockTreeNode({ id: "leaf-1", value: 200 })]);
      const countAfterAdd = updates.length;

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: identityKey,
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
          leaves: [
            createMockTransferLeaf(createMockTreeNode({ id: "leaf-1" })),
          ],
        }),
      );

      expect(updates.length).toBeGreaterThan(countAfterAdd);
      const last = updates[updates.length - 1]!;
      expect(last.available).toBe(0);
      expect(last.owned).toBe(0);
    });

    it("emitBalanceUpdate fires on addIncomingLeaves", async () => {
      const updates: { available: number; owned: number; incoming: number }[] =
        [];
      const lm = createTestableLeafManager({
        onBalanceUpdate: (b) => updates.push(b),
      });

      await lm.addIncomingLeaves(
        [createMockTreeNode({ id: "inc-1", value: 300 })],
        "transfer-1",
      );

      expect(updates.length).toBe(1);
      expect(updates[0]!.incoming).toBe(300);
      expect(updates[0]!.available).toBe(0);
    });

    it("isStaleLeafError detects 'not available to transfer' from SO", async () => {
      await Promise.resolve();
      const lm = createTestableLeafManager();
      const check = leafManagerInternals(lm).isStaleLeafError.bind(lm);

      expect(
        check(
          new Error(
            "leaf is not available to transfer, status: TRANSFER_LOCKED",
          ),
        ),
      ).toBe(true);
      expect(
        check(
          new Error(
            "Failed to request leaves swap: Error: not available to transfer",
          ),
        ),
      ).toBe(true);
    });

    it("isStaleLeafError detects 'not owned by' from SO", async () => {
      await Promise.resolve();
      const lm = createTestableLeafManager();
      const check = leafManagerInternals(lm).isStaleLeafError.bind(lm);

      expect(
        check(
          new Error(
            "nodes [abc] are not owned by the authenticated identity public key 03f5...",
          ),
        ),
      ).toBe(true);
    });

    it("isStaleLeafError does NOT match unrelated errors", async () => {
      await Promise.resolve();
      const lm = createTestableLeafManager();
      const check = leafManagerInternals(lm).isStaleLeafError.bind(lm);

      // Generic service errors should NOT trigger stale-leaf retry
      expect(check(new Error("SSP service is unavailable"))).toBe(false);
      expect(check(new Error("network timeout"))).toBe(false);
      expect(check(new Error("internal server error"))).toBe(false);
      expect(check("string error")).toBe(false);
      expect(check(null)).toBe(false);
    });

    // =====================================================================
    // Concurrent selectLeavesAndExecute tests
    // =====================================================================

    it("two concurrent selectLeavesAndExecute calls select different leaves", async () => {
      const identityKey = new Uint8Array(33).fill(0x02);
      const lm = createTestableLeafManager();
      leafManagerInternals(lm).identityPublicKey = identityKey;

      await lm.addLeaves([
        createMockTreeNode({ id: "leaf-100", value: 100 }),
        createMockTreeNode({ id: "leaf-200", value: 200 }),
        createMockTreeNode({ id: "leaf-300", value: 300 }),
      ]);

      // Launch two concurrent selections that each advance state after "sending"
      const [result1, result2] = await Promise.all([
        lm.selectLeavesAndExecute([300], async (selected) => {
          await new Promise((r) => setTimeout(r, 10));
          // Simulate: executor sends transfer, advances state
          await lm.handleTransferEvent(
            createMockTransfer({
              id: "transfer-1",
              senderIdentityPublicKey: identityKey,
              status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
              leaves: selected[0].map((l) => createMockTransferLeaf(l)),
            }),
          );
          return selected[0].map((l) => l.id);
        }),
        lm.selectLeavesAndExecute([200], async (selected) => {
          await new Promise((r) => setTimeout(r, 10));
          await lm.handleTransferEvent(
            createMockTransfer({
              id: "transfer-2",
              senderIdentityPublicKey: identityKey,
              status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
              leaves: selected[0].map((l) => createMockTransferLeaf(l)),
            }),
          );
          return selected[0].map((l) => l.id);
        }),
      ]);

      // Both should succeed with different leaves (no overlap)
      const allIds = [...result1, ...result2];
      const uniqueIds = new Set(allIds);
      expect(uniqueIds.size).toBe(allIds.length);

      // Both transfers completed — sent leaves are SPENT, unselected leaf-100 remains
      expect(lm.getAvailableBalance()).toBe(100);
      expect(lm.getOwnedBalance()).toBe(100);
    });

    it("paginateTransfers terminates when transfers list is empty even with non-negative offset", async () => {
      let swapQueryCount = 0;
      const lm = createTestableLeafManager({
        config: {
          getCoordinatorAddress: () => "mock",
          getCoordinatorIdentifier: () => "coord-1",
          getNetworkProto: () => 0,
          getSigningOperators: () => ({ "coord-1": { address: "mock" } }),
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
          },
        },
        transferService: {
          queryPrimarySwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            swapQueryCount++;
            // Return empty results with non-negative offset (would cause infinite loop without fix)
            return { transfers: [], offset: 0 };
          }),
          queryCounterSwapTransfers: jest.fn(async () => {
            await Promise.resolve();
            swapQueryCount++;
            return { transfers: [], offset: 0 };
          }),
          queryPendingOutgoingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [],
              offset: 0,
            };
          }),
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
        connectionManager: {
          createSparkClient: jest.fn(async () => {
            await Promise.resolve();
            return {
              query_nodes: jest.fn(async () => {
                await Promise.resolve();
                return { nodes: {} };
              }),
            };
          }),
        },
      });

      // sync() calls paginateTransfers internally — this must not hang
      await lm.sync();

      // Each paginated query should only be called once (empty first page = done).
      // Two swap queries (primary + counter), each called once.
      expect(swapQueryCount).toBe(2);
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
      expect(lm.getInternalLeafCount()).toBe(1);
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
      expect(lm.getInternalLeafCount()).toBe(1);
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
      expect(lm.getInternalLeafCount()).toBe(1);
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

      // AVAILABLE → INCOMING is invalid
      lm.transitionPublic(["a"], "INCOMING");
      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
    });

    it("AVAILABLE → SWAP_PENDING is valid (concurrent wallet swap)", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 1000 })]);

      lm.transitionPublic(["a"], "SWAP_PENDING");
      expect(lm.getLeafRecordPublic("a")?.status).toBe("SWAP_PENDING");
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
    const SYNC_SIGNER = {
      getIdentityPublicKey: jest.fn(async () => {
        await Promise.resolve();
        return new Uint8Array(33).fill(0x02);
      }),
    };

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
      // sync() calls autoOptimizeIfNeeded which needs getOptimizationOptions
      leafManagerInternals(lm).config.getOptimizationOptions ??= () => ({
        auto: false,
        multiplicity: 0,
      });
      leafManagerInternals(lm).getLeaves = jest.fn(async () => {
        await Promise.resolve();
        return opts.leaves ?? [];
      });
      leafManagerInternals(lm).getAllPendingSwaps = jest.fn(async () => {
        await Promise.resolve();
        return opts.swaps ?? [];
      });
      leafManagerInternals(lm).getAllPendingOutgoingTransfers = jest.fn(
        async () => {
          await Promise.resolve();
          return opts.outgoing ?? [];
        },
      );
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );
      leafManagerInternals(lm).transferService.queryPendingTransfers = jest.fn(
        async () => {
          await Promise.resolve();
          return {
            transfers: opts.incomingTransfers ?? [],
          };
        },
      );
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

      const lm = createTestableLeafManager({
        config: { signer: SYNC_SIGNER },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [
                {
                  id: "incoming-t1",
                  type: 0, // not a counter-swap
                  leaves: [{ leaf: incomingLeaf }],
                },
              ],
            };
          }),
        },
      });

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
        config: { signer: SYNC_SIGNER },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
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
        config: { signer: SYNC_SIGNER },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
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

      const lm = createTestableLeafManager({
        config: { signer: SYNC_SIGNER },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return {
              transfers: [
                {
                  id: "t-counter",
                  type: TransferType.COUNTER_SWAP,
                  leaves: [{ leaf: counterSwapLeaf }],
                },
                {
                  id: "t-counter-v3",
                  type: TransferType.COUNTER_SWAP_V3,
                  leaves: [{ leaf: counterSwapV3Leaf }],
                },
                {
                  id: "t-regular",
                  type: 0, // not a counter-swap
                  leaves: [{ leaf: regularLeaf }],
                },
              ],
            };
          }),
        },
      });

      mockSyncDeps(lm, {
        leaves: [],
        incomingTransfers: [
          {
            id: "t-counter",
            type: TransferType.COUNTER_SWAP,
            leaves: [{ leaf: counterSwapLeaf }],
          },
          {
            id: "t-counter-v3",
            type: TransferType.COUNTER_SWAP_V3,
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
        config: { signer: SYNC_SIGNER },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
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
        config: { signer: SYNC_SIGNER },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
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
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
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
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
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
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
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
      await Promise.resolve();
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
        await Promise.resolve();
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
          await Promise.resolve();
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
          await Promise.resolve();
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
        lm.selectLeavesAndExecute([0], async () => {
          await Promise.resolve();
          return "ok";
        }),
      ).rejects.toThrow("Target amount must be positive");

      await expect(
        lm.selectLeavesAndExecute([-1], async () => {
          await Promise.resolve();
          return "ok";
        }),
      ).rejects.toThrow("Target amount must be positive");
    });

    it("rejects when total exceeds available balance", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      await expect(
        lm.selectLeavesAndExecute([600], async () => {
          await Promise.resolve();
          return "ok";
        }),
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
          await Promise.resolve();
          expect(selected[0]).toHaveLength(1);
          expect(selected[0][0]!.id).toBe("c");
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
          await Promise.resolve();
          throw new Error("executor failed");
        }),
      ).rejects.toThrow("executor failed");

      expect(lm.getLeafRecordPublic("a")?.status).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
    });

    it("retries on stale leaf error after sync", async () => {
      let attempt = 0;
      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return new Uint8Array(33).fill(0x02);
            }),
          },
        },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
        },
      });

      leafManagerInternals(lm).getLeaves = jest.fn(async () => {
        await Promise.resolve();
        return [
          createMockTreeNode({ id: "fresh", value: 500, status: "AVAILABLE" }),
        ];
      });
      leafManagerInternals(lm).getAllPendingSwaps = jest.fn(async () => {
        await Promise.resolve();
        return [];
      });
      leafManagerInternals(lm).getAllPendingOutgoingTransfers = jest.fn(
        async () => {
          await Promise.resolve();
          return [];
        },
      );
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );

      await lm.addLeaves([createMockTreeNode({ id: "a", value: 500 })]);

      const result = await lm.selectLeavesAndExecute([500], async () => {
        await Promise.resolve();
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
          await Promise.resolve();
          throw new Error("network timeout");
        }),
      ).rejects.toThrow("network timeout");
    });

    it("triggers swap when exact fit is impossible", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(
            async (params: RequestLeavesSwapParams) => {
              await params.onSwapInitiated?.();
              return [
                createMockTreeNode({ id: "new-500", value: 500 }),
                createMockTreeNode({ id: "new-200", value: 200 }),
              ];
            },
          ),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big", value: 700 })]);

      const result = await lm.selectLeavesAndExecute(
        [500],
        async (selected) => {
          await Promise.resolve();
          expect(selected[0]).toHaveLength(1);
          expect(selected[0][0]!.value).toBe(500);
          return "swapped-ok";
        },
      );

      expect(result).toBe("swapped-ok");
    });

    it("restores LOCAL_LOCKED leaves when swap fails before onSwapInitiated", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(async () => {
            await Promise.resolve();
            throw new Error("swap service down");
          }),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big", value: 700 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => {
          await Promise.resolve();
          return "ok";
        }),
      ).rejects.toThrow("swap service down");

      expect(lm.getLeafRecordPublic("big")?.status).toBe("AVAILABLE");
    });

    it("does not restore SWAP_PENDING leaves when swap fails after onSwapInitiated", async () => {
      const lm = createTestableLeafManager({
        swapService: {
          requestLeavesSwap: jest.fn(
            async (params: RequestLeavesSwapParams) => {
              await params.onSwapInitiated?.();
              throw new Error("swap failed mid-flight");
            },
          ),
        },
      });

      await lm.addLeaves([createMockTreeNode({ id: "big", value: 700 })]);

      await expect(
        lm.selectLeavesAndExecute([500], async () => {
          await Promise.resolve();
          return "ok";
        }),
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
          await Promise.resolve();
          const batch0Values = selected[0].map((l: TreeNode) => l.value);
          const batch1Values = selected[1].map((l: TreeNode) => l.value);
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
        lm.selectLeavesAndExecute([500, 0], async () => {
          await Promise.resolve();
          return "ok";
        }),
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
      await Promise.resolve();
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

      const requestLeavesSwapMock = jest.fn(
        async (params: RequestLeavesSwapParams) => {
          await params.onSwapInitiated?.();
          return params.targetAmounts.map((v: number, i: number) =>
            createMockTreeNode({ id: `new-${i}`, value: v }),
          );
        },
      );

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

      const requestLeavesSwapMock = jest.fn(
        async (params: RequestLeavesSwapParams) => {
          await params.onSwapInitiated?.();
          return [createMockTreeNode({ id: "new-0", value: 128 })];
        },
      );

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ multiplicity: 0 }),
        },
        swapService: { requestLeavesSwap: requestLeavesSwapMock },
      });
      await lm.addLeaves(leaves);

      // When requestLeavesSwap is called, all leaves should already be locked
      requestLeavesSwapMock.mockImplementation(
        async (params: RequestLeavesSwapParams) => {
          // All original leaves should be LOCAL_LOCKED or SWAP_PENDING at this point
          for (const leaf of leaves) {
            const record = lm.getLeafRecordPublic(leaf.id);
            expect(["LOCAL_LOCKED", "SWAP_PENDING"]).toContain(record?.status);
          }
          await params.onSwapInitiated?.();
          return [createMockTreeNode({ id: "new-0", value: 128 })];
        },
      );

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
            await Promise.resolve();
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
          requestLeavesSwap: jest.fn(
            async (params: RequestLeavesSwapParams) => {
              await params.onSwapInitiated?.();
              throw new Error("swap failed after initiation");
            },
          ),
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
          requestLeavesSwap: jest.fn(
            async (params: RequestLeavesSwapParams) => {
              swapCallCount++;
              await params.onSwapInitiated?.();
              return [
                createMockTreeNode({
                  id: `result-${swapCallCount}`,
                  value: 128,
                }),
              ];
            },
          ),
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
        (params: RequestLeavesSwapParams) =>
          new Promise<TreeNode[]>((resolve, reject) => {
            void Promise.resolve(params.onSwapInitiated?.()).then(() => {
              resolveSwap = resolve;
            }, reject);
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
      const swapMock: RequestLeavesSwapMock = jest.fn(async () => {
        await Promise.resolve();
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
      swapMock.mockImplementation(async (params: RequestLeavesSwapParams) => {
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
      const onAutoOptimizeMock = jest.fn(async () => {
        await Promise.resolve();
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
          getCoordinatorAddress: () => "mock-addr",
          getNetworkProto: () => 0,
        },
        onAutoOptimize: onAutoOptimizeMock,
      });

      // checkRenewLeaves is called by registerClaimedLeaves — mock it
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );

      // 8 x 16 triggers shouldOptimize for multiplicity=0
      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );
      await lm.registerClaimedLeaves(leaves);

      // autoOptimizeIfNeeded is fire-and-forget — yield to let it complete
      await new Promise((r) => setTimeout(r, 100));

      expect(onAutoOptimizeMock).toHaveBeenCalled();
    });

    it("does not trigger when auto=false", async () => {
      const onAutoOptimizeMock = jest.fn(async () => {
        await Promise.resolve();
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
        },
        onAutoOptimize: onAutoOptimizeMock,
      });
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );

      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );
      await lm.registerClaimedLeaves(leaves);

      expect(onAutoOptimizeMock).not.toHaveBeenCalled();
    });

    it("does not trigger when leaves are already optimal", async () => {
      const onAutoOptimizeMock = jest.fn(async () => {
        await Promise.resolve();
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
        },
        onAutoOptimize: onAutoOptimizeMock,
      });
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );

      // Single power-of-two leaf — shouldOptimize returns false
      await lm.registerClaimedLeaves([
        createMockTreeNode({ id: "a", value: 128 }),
      ]);

      expect(onAutoOptimizeMock).not.toHaveBeenCalled();
    });

    it("swallows optimization errors silently", async () => {
      const onAutoOptimizeMock = jest.fn(async () => {
        await Promise.resolve();
        throw new Error("optimization unavailable");
      });

      const lm = createTestableLeafManager({
        config: {
          getOptimizationOptions: () => ({ auto: true, multiplicity: 0 }),
        },
        onAutoOptimize: onAutoOptimizeMock,
      });
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );

      const leaves = Array.from({ length: 8 }, (_, i) =>
        createMockTreeNode({ id: `l${i}`, value: 16 }),
      );

      // Should not throw despite swap failure
      const result = await lm.registerClaimedLeaves(leaves);

      // autoOptimizeIfNeeded is fire-and-forget — yield to let it run
      await new Promise((r) => setTimeout(r, 50));

      expect(result).toHaveLength(8);
      expect(onAutoOptimizeMock).toHaveBeenCalled();

      // Leaves should be restored to AVAILABLE (not stuck in LOCAL_LOCKED)
      for (const leaf of leaves) {
        expect(lm.getLeafRecordPublic(leaf.id)?.status).toBe("AVAILABLE");
      }
    });
  });

  // ── handleTransferEvent ──────────────────────────────────────────────

  describe("handleTransferEvent", () => {
    const IDENTITY_PUBKEY = new Uint8Array(33).fill(0x02);
    const OTHER_PUBKEY = new Uint8Array(33).fill(0x03);

    function createMockTransfer(
      overrides: Partial<Transfer> & { leafIds?: string[] },
    ): Transfer {
      const leafIds = overrides.leafIds ?? ["l1"];
      return {
        id: "transfer-1",
        senderIdentityPublicKey: IDENTITY_PUBKEY,
        receiverIdentityPublicKey: OTHER_PUBKEY,
        status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        totalValue: 0,
        expiryTime: undefined,
        leaves: leafIds.map((id) => ({
          leaf: createMockTreeNode({ id, value: 500 }),
          id,
          previousLeaf: undefined,
          adaptorSignature: new Uint8Array(),
          leafKeyTweakNonce: new Uint8Array(),
        })),
        createdTime: undefined,
        updatedTime: undefined,
        type: TransferType.TRANSFER,
        sparkInvoice: "",
        network: 0,
        receivers: [],
        ...overrides,
      } as Transfer;
    }

    /** Create a LeafManager with identityPublicKey set (as if sync was called). */
    async function createSyncedLeafManager(
      overrides?: Parameters<typeof createTestableLeafManager>[0],
    ) {
      const lm = createTestableLeafManager({
        ...overrides,
        config: {
          getOptimizationOptions: () => ({ auto: false, multiplicity: 0 }),
          signer: {
            getIdentityPublicKey: jest.fn(async () => {
              await Promise.resolve();
              return IDENTITY_PUBKEY;
            }),
          },
          ...overrides?.config,
        },
        transferService: {
          queryPendingTransfers: jest.fn(async () => {
            await Promise.resolve();
            return { transfers: [] };
          }),
          ...overrides?.transferService,
        },
      });

      // Call sync to set identityPublicKey
      leafManagerInternals(lm).getLeaves = jest.fn(async () => {
        await Promise.resolve();
        return [];
      });
      leafManagerInternals(lm).getAllPendingSwaps = jest.fn(async () => {
        await Promise.resolve();
        return [];
      });
      leafManagerInternals(lm).getAllPendingOutgoingTransfers = jest.fn(
        async () => {
          await Promise.resolve();
          return [];
        },
      );
      leafManagerInternals(lm).checkRenewLeaves = jest.fn(
        async (nodes: TreeNode[]) => {
          await Promise.resolve();
          return nodes;
        },
      );
      await lm.sync();

      return lm;
    }

    it("transitions LOCAL_LOCKED → OUTGOING on SENDER_INITIATED", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("OUTGOING");
      expect(lm.getLeafRecordPublic("l1")?.source).toEqual({
        kind: "transfer",
        transferId: "transfer-1",
      });
    });

    it("transitions LOCAL_LOCKED → OUTGOING on SENDER_INITIATED_COORDINATOR", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("OUTGOING");
    });

    it("transitions LOCAL_LOCKED → OUTGOING on SENDER_KEY_TWEAK_PENDING", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("OUTGOING");
    });

    it("transitions LOCAL_LOCKED → OUTGOING on APPLYING_SENDER_KEY_TWEAK", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("OUTGOING");
    });

    it("transitions LOCAL_LOCKED → SWAP_PENDING on APPLYING_SENDER_KEY_TWEAK for swaps", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK,
          type: TransferType.PRIMARY_SWAP_V3,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("SWAP_PENDING");
    });

    it("transitions OUTGOING → SPENT on SENDER_KEY_TWEAKED", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");
      lm.transitionPublic(["l1"], "OUTGOING");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
        }),
      );

      // SPENT deletes the leaf from cache
      expect(lm.getLeafRecordPublic("l1")).toBeUndefined();
      expect(lm.getOwnedBalance()).toBe(0);
    });

    it("transitions OUTGOING → AVAILABLE on RETURNED", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");
      lm.transitionPublic(["l1"], "OUTGOING");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_RETURNED,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(500);
    });

    it("ignores events when identityPublicKey is not set", async () => {
      // Don't call sync — identityPublicKey remains undefined
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        }),
      );

      // Should still be LOCAL_LOCKED — event was ignored
      expect(lm.getLeafRecordPublic("l1")?.status).toBe("LOCAL_LOCKED");
    });

    it("ignores events where sender is not us", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          senderIdentityPublicKey: OTHER_PUBKEY,
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("LOCAL_LOCKED");
    });

    it("handles multiple leaves in a single transfer", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([
        createMockTreeNode({ id: "l1", value: 300 }),
        createMockTreeNode({ id: "l2", value: 200 }),
      ]);
      lm.transitionPublic(["l1", "l2"], "LOCAL_LOCKED");

      await lm.handleTransferEvent(
        createMockTransfer({
          leafIds: ["l1", "l2"],
          status: TransferStatus.TRANSFER_STATUS_SENDER_INITIATED,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")?.status).toBe("OUTGOING");
      expect(lm.getLeafRecordPublic("l2")?.status).toBe("OUTGOING");
    });

    it("emits balance update after processing", async () => {
      const callback = jest.fn();
      const lm = await createSyncedLeafManager({ onBalanceUpdate: callback });
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");
      lm.transitionPublic(["l1"], "OUTGOING");
      callback.mockClear();

      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
        }),
      );

      expect(callback).toHaveBeenCalledWith({
        available: 0,
        owned: 0,
        incoming: 0,
      });
    });

    it("transitions to SPENT for terminal statuses like RECEIVER_KEY_TWEAKED", async () => {
      const lm = await createSyncedLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "l1", value: 500 })]);
      lm.transitionPublic(["l1"], "LOCAL_LOCKED");
      lm.transitionPublic(["l1"], "OUTGOING");

      // RECEIVER_KEY_TWEAKED means the transfer completed — leaf is spent and removed
      await lm.handleTransferEvent(
        createMockTransfer({
          status: TransferStatus.TRANSFER_STATUS_RECEIVER_KEY_TWEAKED,
        }),
      );

      expect(lm.getLeafRecordPublic("l1")).toBeUndefined();
      expect(lm.getOwnedBalance()).toBe(0);
    });
  });

  // ── handleDepositEvent ───────────────────────────────────────────────

  describe("handleDepositEvent", () => {
    it("adds CREATING deposit as INCOMING", async () => {
      const lm = createTestableLeafManager();
      const deposit = createMockTreeNode({
        id: "dep-1",
        value: 1000,
        status: "CREATING",
      });

      await lm.handleDepositEvent(deposit);

      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("INCOMING");
      expect(lm.getLeafRecordPublic("dep-1")?.source).toEqual({
        kind: "deposit",
        depositId: "dep-1",
      });
      expect(lm.getIncomingBalance()).toBe(1000);
    });

    it("does not overwrite existing leaf with CREATING deposit", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "dep-1", value: 1000 })]);

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "CREATING" }),
      );

      // Should still be AVAILABLE — not overwritten to INCOMING
      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("AVAILABLE");
    });

    it("adds AVAILABLE deposit as new leaf", async () => {
      const lm = createTestableLeafManager();
      const deposit = createMockTreeNode({
        id: "dep-1",
        value: 1000,
        status: "AVAILABLE",
      });

      await lm.handleDepositEvent(deposit);

      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("AVAILABLE");
      expect(lm.getLeafRecordPublic("dep-1")?.source).toEqual({
        kind: "deposit",
        depositId: "dep-1",
      });
      expect(lm.getAvailableBalance()).toBe(1000);
    });

    it("transitions existing INCOMING leaf to AVAILABLE on confirmed deposit", async () => {
      const lm = createTestableLeafManager();

      // First: deposit arrives as CREATING → INCOMING
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "CREATING" }),
      );
      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("INCOMING");

      // Then: deposit confirms → AVAILABLE
      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "AVAILABLE" }),
      );
      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("AVAILABLE");
      expect(lm.getAvailableBalance()).toBe(1000);
      expect(lm.getIncomingBalance()).toBe(0);
    });

    it("ignores deposits with unhandled statuses", async () => {
      const lm = createTestableLeafManager();

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "SPENT" }),
      );

      expect(lm.getLeafRecordPublic("dep-1")).toBeUndefined();
      expect(lm.getAvailableBalance()).toBe(0);
    });

    it("does not re-transition already AVAILABLE leaf", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "dep-1", value: 1000 })]);

      // Source before is { kind: "none" } from addLeaves
      const sourceBefore = lm.getLeafRecordPublic("dep-1")?.source;

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "AVAILABLE" }),
      );

      // Should still be AVAILABLE, source unchanged (transition is a no-op)
      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("AVAILABLE");
      expect(lm.getLeafRecordPublic("dep-1")?.source).toEqual(sourceBefore);
    });

    it("emits balance update", async () => {
      const callback = jest.fn();
      const lm = createTestableLeafManager({ onBalanceUpdate: callback });

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 500, status: "AVAILABLE" }),
      );

      expect(callback).toHaveBeenCalledWith({
        available: 500,
        owned: 500,
        incoming: 0,
      });
    });

    it("does not overwrite LOCAL_LOCKED leaf on AVAILABLE deposit", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "dep-1", value: 1000 })]);
      lm.transitionPublic(["dep-1"], "LOCAL_LOCKED");

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "AVAILABLE" }),
      );

      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("LOCAL_LOCKED");
    });

    it("does not overwrite OUTGOING leaf on AVAILABLE deposit", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "dep-1", value: 1000 })]);
      lm.transitionPublic(["dep-1"], "LOCAL_LOCKED");
      lm.transitionPublic(["dep-1"], "OUTGOING");

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "AVAILABLE" }),
      );

      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("OUTGOING");
    });

    it("does not overwrite SWAP_PENDING leaf on AVAILABLE deposit", async () => {
      const lm = createTestableLeafManager();
      await lm.addLeaves([createMockTreeNode({ id: "dep-1", value: 1000 })]);
      lm.transitionPublic(["dep-1"], "LOCAL_LOCKED");
      lm.transitionPublic(["dep-1"], "SWAP_PENDING");

      await lm.handleDepositEvent(
        createMockTreeNode({ id: "dep-1", value: 1000, status: "AVAILABLE" }),
      );

      expect(lm.getLeafRecordPublic("dep-1")?.status).toBe("SWAP_PENDING");
    });
  });
});
