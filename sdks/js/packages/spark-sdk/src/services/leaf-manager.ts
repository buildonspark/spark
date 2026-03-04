import { equalBytes } from "@noble/curves/utils";
import { Mutex } from "async-mutex";
import { SparkValidationError } from "../errors/index.js";
import {
  QueryNodesRequest,
  QueryNodesResponse,
  TransferType,
  TreeNode,
  TreeNodeStatus,
} from "../proto/spark.js";
import { KeyDerivation, KeyDerivationType } from "../signer/types.js";
import {
  doesTxnNeedRenewed,
  getTxFromRawTxBytes,
  isZeroTimelock,
} from "../utils/index.js";
import { addPublicKeys } from "../utils/keys.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection/connection.js";
import SwapService from "./swap.js";
import { LeafKeyTweak, TransferService } from "./transfer.js";

// TODO: Implement LeafSource, LeafStatus, LeafRecord
type LeafSource =
  | { kind: "transfer"; transferId: string }
  | { kind: "swap"; swapId: string }
  | { kind: "deposit"; depositId: string }
  | { kind: "none" };

enum LeafStatus {
  AVAILABLE = "AVAILABLE",
  LOCAL_LOCKED = "LOCAL_LOCKED",
  OUTGOING = "OUTGOING",
  SWAP_PENDING = "SWAP_PENDING",
  INCOMING = "INCOMING",
  SPENT = "SPENT",
}

type LeafRecord = {
  treeNode: TreeNode;
  status: LeafStatus;
  source: LeafSource;

  lockId?: string;
  lockExpiresAt?: number;
  lastUpdated?: number;
};

const VALID_TRANSITIONS: Record<LeafStatus, LeafStatus[]> = {
  [LeafStatus.AVAILABLE]: [LeafStatus.LOCAL_LOCKED],
  [LeafStatus.LOCAL_LOCKED]: [
    LeafStatus.AVAILABLE,
    LeafStatus.OUTGOING,
    LeafStatus.SWAP_PENDING,
  ],
  [LeafStatus.OUTGOING]: [LeafStatus.AVAILABLE, LeafStatus.SPENT],
  [LeafStatus.SWAP_PENDING]: [LeafStatus.AVAILABLE, LeafStatus.SPENT],
  [LeafStatus.INCOMING]: [LeafStatus.AVAILABLE],
  [LeafStatus.SPENT]: [],
};

// Only LOCAL_LOCKED is preserved across sync — it's the only status where the SO
// hasn't been contacted yet.
const SYNC_PRESERVED_STATUSES = new Set([LeafStatus.LOCAL_LOCKED]);

// Statuses where a local or remote operation is in progress — addLeaves must not
// overwrite these, as that would corrupt in-flight state.
const IN_FLIGHT_STATUSES = new Set([
  LeafStatus.LOCAL_LOCKED,
  LeafStatus.OUTGOING,
  LeafStatus.SWAP_PENDING,
]);
const OWNED_STATUSES = new Set([
  LeafStatus.AVAILABLE,
  LeafStatus.LOCAL_LOCKED,
  LeafStatus.OUTGOING,
  LeafStatus.SWAP_PENDING,
]);

export type BalanceSnapshot = {
  available: number;
  owned: number;
  incoming: number;
};

export type OnBalanceUpdate = (balance: BalanceSnapshot) => void;
export default class LeafManager {
  private leaves: Map<string, LeafRecord> = new Map();

  private leavesMutex = new Mutex();

  constructor(
    private readonly config: WalletConfigService,
    private readonly swapService: SwapService,
    private readonly transferService: TransferService,
    private readonly connectionManager: ConnectionManager,
    private readonly onBalanceUpdate?: OnBalanceUpdate,
  ) {}

  private emitBalanceUpdate(): void {
    this.onBalanceUpdate?.({
      available: this.getAvailableBalance(),
      owned: this.getOwnedBalance(),
      incoming: this.getIncomingBalance(),
    });
  }

  // #region Public API
  public async sync() {
    const [rawLeaves, swaps, outgoingTransfers, incomingTransfers] =
      await Promise.all([
        this.getLeaves(),
        this.getAllPendingSwaps(),
        this.getAllPendingOutgoingTransfers(),
        this.transferService.queryPendingTransfers(),
      ]);

    const leaves = await this.checkRenewLeaves(rawLeaves);

    await this.leavesMutex.runExclusive(() => {
      const preserved = new Map<string, LeafRecord>();
      for (const [id, record] of this.leaves) {
        if (SYNC_PRESERVED_STATUSES.has(record.status)) {
          preserved.set(id, record);
        }
      }

      this.leaves.clear();

      for (const leaf of leaves) {
        if (leaf.status === "AVAILABLE") {
          this.leaves.set(leaf.id, {
            treeNode: leaf,
            status: LeafStatus.AVAILABLE,
            source: { kind: "none" },
          });
        }
      }

      for (const { leaf, transferId } of swaps) {
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.SWAP_PENDING,
          source: { kind: "swap", swapId: transferId },
        });
      }

      for (const { leaf, transferId } of outgoingTransfers) {
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.OUTGOING,
          source: { kind: "transfer", transferId },
        });
      }

      for (const transfer of incomingTransfers.transfers) {
        // Counter-swaps are the inbound side of a swap we initiated — they're
        // already accounted for in SWAP_PENDING (owned balance). Including them
        // as INCOMING would double-count the sats.
        if (
          transfer.type === TransferType.COUNTER_SWAP ||
          transfer.type === TransferType.COUNTER_SWAP_V3
        ) {
          continue;
        }
        for (const leaf of transfer.leaves) {
          if (!leaf.leaf) continue;
          // Don't downgrade OUTGOING/SWAP_PENDING to INCOMING (e.g., self-transfers
          // appear in both outgoing and incoming queries).
          const existing = this.leaves.get(leaf.leaf.id);
          if (
            existing &&
            (existing.status === LeafStatus.OUTGOING ||
              existing.status === LeafStatus.SWAP_PENDING)
          ) {
            continue;
          }
          this.leaves.set(leaf.leaf.id, {
            treeNode: leaf.leaf,
            status: LeafStatus.INCOMING,
            source: { kind: "transfer", transferId: transfer.id },
          });
        }
      }

      // In-flight local state always wins over server state. If we have a leaf
      // as LOCAL_LOCKED, the SO hasn't been contacted yet (e.g., a swap is being
      // initiated). Restoring it unconditionally ensures the leaf stays locked
      // until the calling code explicitly transitions it.
      for (const [id, record] of preserved) {
        this.leaves.set(id, record);
      }

      this.emitBalanceUpdate();
    });
  }

  public async getLeaves(isBalanceCheck: boolean = false): Promise<TreeNode[]> {
    const ownerIdentityPubkey = await this.config.signer.getIdentityPublicKey();
    const coordinatorId = this.config.getCoordinatorIdentifier();
    const network = this.config.getNetworkProto();

    let operators = Object.entries(this.config.getSigningOperators());
    if (isBalanceCheck) {
      operators = operators.filter(([id]) => id === coordinatorId);
    }

    const operatorToLeaves = new Map<string, QueryNodesResponse>();
    await Promise.all(
      operators.map(async ([id, operator]) => {
        const leaves = await this.queryNodes(
          {
            source: { $case: "ownerIdentityPubkey", ownerIdentityPubkey },
            includeParents: false,
            network,
            statuses: [TreeNodeStatus.TREE_NODE_STATUS_AVAILABLE],
          },
          operator.address,
        );
        operatorToLeaves.set(id, leaves);
      }),
    );

    const coordinatorLeaves = operatorToLeaves.get(coordinatorId);
    if (coordinatorLeaves === undefined) {
      throw new SparkValidationError("Coordinator leaves not found", {
        field: "coordinatorLeaves",
      });
    }

    const outOfSyncIds = new Set<string>();
    if (!isBalanceCheck) {
      for (const [opId, opLeaves] of operatorToLeaves) {
        if (opId === coordinatorId) continue;
        for (const [nodeId, leaf] of Object.entries(coordinatorLeaves.nodes)) {
          const opLeaf = opLeaves.nodes[nodeId];
          if (!this.isLeafConsistent(leaf, opLeaf)) {
            outOfSyncIds.add(nodeId);
          }
        }
      }
    }

    // Defensive: queryNodes already filters for AVAILABLE, but double-check
    // in case the server returns unexpected statuses. Out-of-sync leaves are
    // excluded intentionally — their state is inconsistent across SOs, so
    // recovery could worsen the inconsistency. They'll be resolved on next sync.
    const candidates = Object.values(coordinatorLeaves.nodes).filter(
      (node) => node.status === "AVAILABLE" && !outOfSyncIds.has(node.id),
    );

    const actions = await Promise.all(
      candidates.map(async (leaf) => {
        if (leaf.parentNodeId) {
          const parentPubkey =
            await this.config.signer.getPublicKeyFromDerivation({
              type: KeyDerivationType.LEAF,
              path: leaf.parentNodeId,
            });
          if (
            this.verifyKey(
              parentPubkey,
              leaf.signingKeyshare?.publicKey ?? new Uint8Array(),
              leaf.verifyingPublicKey,
            )
          ) {
            return { type: "RECOVER", leaf, path: leaf.parentNodeId } as const;
          }
        }

        const leafPubkey = await this.config.signer.getPublicKeyFromDerivation({
          type: KeyDerivationType.LEAF,
          path: leaf.id,
        });

        return this.verifyKey(
          leafPubkey,
          leaf.signingKeyshare?.publicKey ?? new Uint8Array(),
          leaf.verifyingPublicKey,
        )
          ? ({ type: "VALID", leaf } as const)
          : ({ type: "INVALID" } as const);
      }),
    );

    const validLeaves: TreeNode[] = [];
    const recoverByPath = new Map<string, TreeNode[]>();

    for (const action of actions) {
      if (action.type === "VALID") {
        validLeaves.push(action.leaf);
      } else if (action.type === "RECOVER") {
        const existing = recoverByPath.get(action.path) ?? [];
        existing.push(action.leaf);
        recoverByPath.set(action.path, existing);
      }
    }

    // Recovery is awaited (unlike the original fire-and-forget in spark-wallet.ts)
    // so that recovered leaves are included in this call's results. The try/catch
    // ensures a failed recovery doesn't drop the already-collected valid leaves.
    const finalLeaves: TreeNode[] = [...validLeaves];
    for (const [path, leaves] of recoverByPath) {
      try {
        const recovered = await this.recoverLeaves(leaves, {
          type: KeyDerivationType.LEAF,
          path,
        });
        finalLeaves.push(...recovered);
      } catch (err) {
        // Recovery failed — skip these leaves rather than losing all valid leaves.
      }
    }

    return finalLeaves;
  }

  public async addLeaves(leaves: TreeNode[]) {
    await this.leavesMutex.runExclusive(() => {
      for (const leaf of leaves) {
        const existing = this.leaves.get(leaf.id);
        if (existing && IN_FLIGHT_STATUSES.has(existing.status)) continue;
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.AVAILABLE,
          source: { kind: "none" },
        });
      }
      this.emitBalanceUpdate();
    });
  }

  /** Add leaves as INCOMING (unclaimed transfer or unconfirmed deposit).
   *  Does not overwrite leaves already in the cache with a non-INCOMING status. */
  public async addIncomingLeaves(leaves: TreeNode[], transferId: string) {
    await this.leavesMutex.runExclusive(() => {
      for (const leaf of leaves) {
        const existing = this.leaves.get(leaf.id);
        if (existing && existing.status !== LeafStatus.INCOMING) continue;
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.INCOMING,
          source: { kind: "transfer", transferId },
        });
      }
      this.emitBalanceUpdate();
    });
  }

  public async removeLeaves(leafIds: string[]) {
    await this.leavesMutex.runExclusive(() => {
      for (const id of leafIds) {
        this.leaves.delete(id);
      }
      this.emitBalanceUpdate();
    });
  }

  /** Register newly claimed leaves — renews them and adds to cache.
   *  Unconditionally sets status to AVAILABLE, bypassing the IN_FLIGHT_STATUSES
   *  guard in addLeaves, since successfully claimed leaves are definitively ours. */
  public async registerClaimedLeaves(leaves: TreeNode[]): Promise<TreeNode[]> {
    const renewed = await this.checkRenewLeaves(leaves);
    await this.leavesMutex.runExclusive(() => {
      for (const leaf of renewed) {
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.AVAILABLE,
          source: { kind: "none" },
        });
      }
      this.emitBalanceUpdate();
    });
    return renewed;
  }

  public getAvailableBalance(): number {
    let total = 0;
    for (const record of this.leaves.values()) {
      if (record.status === LeafStatus.AVAILABLE)
        total += record.treeNode.value;
    }
    return total;
  }

  public getOwnedBalance(): number {
    let total = 0;
    for (const record of this.leaves.values()) {
      if (OWNED_STATUSES.has(record.status)) total += record.treeNode.value;
    }
    return total;
  }

  public getIncomingBalance(): number {
    let total = 0;
    for (const record of this.leaves.values()) {
      if (record.status === LeafStatus.INCOMING) total += record.treeNode.value;
    }
    return total;
  }
  // #endregion

  // #region State Management
  /**
   * Transition one or more leaves to a new status.
   *
   * Resilient by design — this is a local cache, not the source of truth:
   * - Unknown leaf ids are skipped (next sync() will pick them up).
   */
  private transition(
    leafIds: string[],
    toStatus: LeafStatus,
    meta?: { source: LeafSource },
  ): void {
    for (const leafId of leafIds) {
      const leaf = this.leaves.get(leafId);
      if (!leaf) {
        continue;
      }

      const allowed = VALID_TRANSITIONS[leaf.status];
      if (!allowed.includes(toStatus)) {
        continue;
      }

      if (toStatus === LeafStatus.SPENT) {
        this.leaves.delete(leafId);
        continue;
      }

      leaf.status = toStatus;
      if (meta?.source !== undefined) leaf.source = meta.source;
    }
  }
  // #endregion

  // #region Leaf Renewal
  private async checkRenewLeaves(nodes: TreeNode[]): Promise<TreeNode[]> {
    const nodesToRenewNodeTxn: TreeNode[] = [];
    const nodesToRenewRefundTxn: TreeNode[] = [];
    const nodesToRenewZeroTimelockTxn: TreeNode[] = [];
    const nodeIds: string[] = [];
    const validNodes: TreeNode[] = [];

    for (const node of nodes) {
      try {
        const nodeTx = getTxFromRawTxBytes(node.nodeTx);
        const refundTx = getTxFromRawTxBytes(node.refundTx);

        if (!nodeTx.inputsLength) {
          throw new SparkValidationError("Invalid node transaction", {
            field: "inputsLength",
            value: nodeTx.inputsLength,
            expected: "Non-zero inputs length",
          });
        }
        if (!refundTx.inputsLength) {
          throw new SparkValidationError("Invalid refund transaction", {
            field: "inputsLength",
            value: refundTx.inputsLength,
            expected: "Non-zero inputs length",
          });
        }

        const nodeSequence = nodeTx.getInput(0).sequence;
        const refundSequence = refundTx.getInput(0).sequence;

        if (nodeSequence === undefined) {
          throw new SparkValidationError("Invalid node transaction", {
            field: "sequence",
            value: nodeTx.getInput(0),
            expected: "Non-null sequence",
          });
        }
        if (refundSequence === undefined) {
          throw new SparkValidationError("Invalid refund transaction", {
            field: "sequence",
            value: refundTx.getInput(0),
            expected: "Non-null sequence",
          });
        }

        if (doesTxnNeedRenewed(refundSequence)) {
          if (isZeroTimelock(nodeSequence)) {
            nodesToRenewZeroTimelockTxn.push(node);
          } else if (doesTxnNeedRenewed(nodeSequence)) {
            nodesToRenewNodeTxn.push(node);
          } else {
            nodesToRenewRefundTxn.push(node);
          }
          nodeIds.push(node.id);
        } else {
          validNodes.push(node);
        }
      } catch (err) {
        // Skip this node — don't let one malformed leaf abort the entire batch.
        console.warn(
          `[LeafManager] checkRenewLeaves validation failed for node ${node.id}`,
          err,
        );
      }
    }

    if (
      nodesToRenewNodeTxn.length === 0 &&
      nodesToRenewRefundTxn.length === 0 &&
      nodesToRenewZeroTimelockTxn.length === 0
    ) {
      return validNodes;
    }

    const nodesResp = await this.queryNodes({
      source: { $case: "nodeIds", nodeIds: { nodeIds } },
      includeParents: true,
      network: this.config.getNetworkProto(),
      statuses: [],
    });

    const nodesMap = new Map<string, TreeNode>();
    for (const node of Object.values(nodesResp.nodes)) {
      nodesMap.set(node.id, node);
    }

    await Promise.all([
      ...nodesToRenewNodeTxn.map(async (node) => {
        try {
          const parentNode = this.requireParentNode(node, nodesMap);
          const renewedNode = await this.transferService.renewNodeTxn(
            node,
            parentNode,
          );
          validNodes.push(renewedNode);
        } catch (err) {
          // Skip — don't let one failed renewal discard the rest.
          console.warn(
            `[LeafManager] renewNodeTxn failed for node ${node.id}`,
            err,
          );
        }
      }),
      ...nodesToRenewRefundTxn.map(async (node) => {
        try {
          const parentNode = this.requireParentNode(node, nodesMap);
          const renewedNode = await this.transferService.renewRefundTxn(
            node,
            parentNode,
          );
          validNodes.push(renewedNode);
        } catch (err) {
          // Skip — don't let one failed renewal discard the rest.
          console.warn(
            `[LeafManager] renewRefundTxn failed for node ${node.id}`,
            err,
          );
        }
      }),
      ...nodesToRenewZeroTimelockTxn.map(async (node) => {
        try {
          const renewedNode =
            await this.transferService.renewZeroTimelockNodeTxn(node);
          validNodes.push(renewedNode);
        } catch (err) {
          // Skip — don't let one failed renewal discard the rest.
          console.warn(
            `[LeafManager] renewZeroTimelockNodeTxn failed for node ${node.id}`,
            err,
          );
        }
      }),
    ]);

    return validNodes;
  }

  private requireParentNode(
    node: TreeNode,
    nodesMap: Map<string, TreeNode>,
  ): TreeNode {
    if (!node.parentNodeId) {
      throw new Error(`node ${node.id} has no parent`);
    }
    const parentNode = nodesMap.get(node.parentNodeId);
    if (!parentNode) {
      throw new Error(`parent node ${node.parentNodeId} not found`);
    }
    return parentNode;
  }
  // #endregion

  // #region Network Queries
  private async queryNodes(
    baseRequest: Omit<QueryNodesRequest, "limit" | "offset">,
    sparkClientAddress?: string,
    pageSize: number = 100,
  ): Promise<QueryNodesResponse> {
    const address = sparkClientAddress ?? this.config.getCoordinatorAddress();
    const aggregatedNodes: {
      [key: string]: QueryNodesResponse["nodes"][string];
    } = {};
    let offset = 0;

    while (true) {
      const sparkClient =
        await this.connectionManager.createSparkClient(address);
      const response = await sparkClient.query_nodes({
        ...baseRequest,
        limit: pageSize,
        offset,
      });

      Object.assign(aggregatedNodes, response.nodes ?? {});

      const received = Object.keys(response.nodes ?? {}).length;
      if (received < pageSize || baseRequest.source?.$case === "nodeIds") {
        return {
          nodes: aggregatedNodes,
          offset: response.offset,
        } as QueryNodesResponse;
      }
      offset += pageSize;
    }
  }

  private async getAllPendingSwaps(): Promise<
    { leaf: TreeNode; transferId: string }[]
  > {
    const extractLeaves = (transfer: {
      id: string;
      leaves: { leaf: TreeNode | undefined }[];
    }) =>
      transfer.leaves.flatMap((leaf) =>
        leaf.leaf ? [{ leaf: leaf.leaf, transferId: transfer.id }] : [],
      );

    // A swap has up to 2 transfers: the primary (outgoing) and the counter
    // (incoming replacement). The primary query filters for pre-SENDER_KEY_TWEAKED
    // statuses, so once the primary advances to SENDER_KEY_TWEAKED (which atomically
    // creates the counter swap), it drops out of the primary query. No overlap.
    const [primarySwaps, counterSwaps] = await Promise.all([
      this.paginateTransfers(
        (params) => this.transferService.queryPrimarySwapTransfers(params),
        extractLeaves,
      ),
      this.paginateTransfers(
        (params) => this.transferService.queryCounterSwapTransfers(params),
        extractLeaves,
      ),
    ]);

    return [...primarySwaps, ...counterSwaps];
  }

  private async getAllPendingOutgoingTransfers(): Promise<
    { leaf: TreeNode; transferId: string }[]
  > {
    return this.paginateTransfers(
      (params) => this.transferService.queryPendingOutgoingTransfers(params),
      (transfer) =>
        transfer.leaves.flatMap((leaf) =>
          leaf.leaf ? [{ leaf: leaf.leaf, transferId: transfer.id }] : [],
        ),
    );
  }

  private async paginateTransfers<T extends { id: string }>(
    query: (params: {
      limit: number;
      offset: number;
    }) => Promise<{ transfers: T[]; offset: number }>,
    extractLeaves: (transfer: T) => { leaf: TreeNode; transferId: string }[],
  ): Promise<{ leaf: TreeNode; transferId: string }[]> {
    const PAGE_SIZE = 100;
    const results: { leaf: TreeNode; transferId: string }[] = [];
    let offset = 0;
    let prevOffset = -1;
    do {
      const response = await query({ limit: PAGE_SIZE, offset });
      for (const transfer of response.transfers) {
        results.push(...extractLeaves(transfer));
      }
      if (response.transfers.length < PAGE_SIZE) break;
      if (response.offset === prevOffset) break; // no forward progress
      prevOffset = response.offset;
      offset = response.offset;
    } while (offset >= 0);
    return results;
  }
  // #endregion

  // #region Recovery
  private async recoverLeaves(
    leaves: TreeNode[],
    keyDerivation: KeyDerivation,
  ): Promise<TreeNode[]> {
    const leafKeyTweaks: LeafKeyTweak[] = leaves.map((leaf) => ({
      leaf,
      keyDerivation,
      newKeyDerivation: { type: KeyDerivationType.RANDOM },
    }));

    const transfer = await this.transferService.sendTransferWithKeyTweaks(
      leafKeyTweaks,
      await this.config.signer.getIdentityPublicKey(),
    );

    const pendingTransfer = await this.transferService.queryTransfer(
      transfer.id,
    );
    return pendingTransfer
      ? await this.transferService.claimTransfer(pendingTransfer)
      : [];
  }
  // #endregion

  // #region Filtering & Validation
  private verifyKey(
    pubkey1: Uint8Array,
    pubkey2: Uint8Array,
    verifyingKey: Uint8Array,
  ): boolean {
    return equalBytes(addPublicKeys(pubkey1, pubkey2), verifyingKey);
  }

  private isLeafConsistent(
    leaf: TreeNode,
    opLeaf: TreeNode | undefined,
  ): boolean {
    if (!opLeaf) return false;
    return (
      leaf.status === opLeaf.status &&
      !!leaf.signingKeyshare &&
      !!opLeaf.signingKeyshare &&
      equalBytes(
        leaf.signingKeyshare.publicKey,
        opLeaf.signingKeyshare.publicKey,
      ) &&
      equalBytes(leaf.nodeTx, opLeaf.nodeTx)
    );
  }
  // #endregion
}
