import type { Logger } from "@lightsparkdev/core";
import { equalBytes } from "@noble/curves/utils";
import { Mutex } from "async-mutex";
import { SparkValidationError } from "../errors/index.js";
import {
  QueryNodesRequest,
  QueryNodesResponse,
  Transfer,
  TransferStatus,
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
import { LoggingService } from "../utils/logging-service.js";
import { optimize, shouldOptimize } from "../utils/optimize.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection/connection.js";
import SwapService from "./swap.js";
import { LeafKeyTweak, TransferService } from "./transfer.js";

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
};

const VALID_TRANSITIONS: Record<LeafStatus, LeafStatus[]> = {
  // AVAILABLE → OUTGOING/SWAP_PENDING/SPENT: concurrent wallet case only —
  // another instance sent a transfer or swap using this leaf, we receive the
  // stream event while the leaf is still AVAILABLE in our cache (we never
  // locked it locally).
  [LeafStatus.AVAILABLE]: [
    LeafStatus.LOCAL_LOCKED,
    LeafStatus.OUTGOING,
    LeafStatus.SWAP_PENDING,
    LeafStatus.SPENT,
  ],
  [LeafStatus.LOCAL_LOCKED]: [
    LeafStatus.AVAILABLE,
    LeafStatus.OUTGOING,
    LeafStatus.SWAP_PENDING,
    LeafStatus.SPENT,
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
  private optimizationInProgress = false;
  private hasSynced = false;
  private leaves: Map<string, LeafRecord> = new Map();

  // Mutex policy: acquire when transitioning AVAILABLE → LOCAL_LOCKED (prevents
  // double-selection) or when inserting/removing leaves from the map. Read-only
  // operations (balance getters, selectLeavesReadOnly) and error-path restores
  // (restoreLocalLockedToAvailable) do not acquire — JS single-threading guarantees
  // synchronous iterations can't be interleaved.
  private leavesMutex = new Mutex();
  private identityPublicKey: Uint8Array | undefined;
  private readonly logger: Logger;

  constructor(
    private readonly config: WalletConfigService,
    private readonly swapService: SwapService,
    private readonly transferService: TransferService,
    private readonly connectionManager: ConnectionManager,
    private readonly onBalanceUpdate?: OnBalanceUpdate,
    private readonly onAutoOptimize?: () => Promise<void>,
    logging = LoggingService.fromConfig(config),
  ) {
    this.logger = logging.logger("LeafManager");
    logging.wrapPrototypeMethods("LeafManager", this);
  }

  private log(functionName: string, message: string): void {
    this.logger.trace(`${functionName}: ${message}`);
  }

  private emitBalanceUpdate(): void {
    this.onBalanceUpdate?.({
      available: this.getAvailableBalance(),
      owned: this.getOwnedBalance(),
      incoming: this.getIncomingBalance(),
    });
  }

  // ---------------------------------------------------------------------------
  // Public API
  // ---------------------------------------------------------------------------

  /** Must be called before stream events are processed. Sets the identity
   *  public key used by handleTransferEvent to filter sender events. */
  public async initialize() {
    this.identityPublicKey = await this.config.signer.getIdentityPublicKey();
  }

  public async sync() {
    this.identityPublicKey = await this.config.signer.getIdentityPublicKey();

    const prevBalance = {
      available: this.getAvailableBalance(),
      owned: this.getOwnedBalance(),
      incoming: this.getIncomingBalance(),
      count: this.leaves.size,
    };
    this.log(
      "sync",
      `Starting sync. Pre-sync: ${prevBalance.count} leaves, available=${prevBalance.available} owned=${prevBalance.owned} incoming=${prevBalance.incoming}`,
    );

    const [rawLeaves, swaps, outgoingTransfers, incomingTransfers] =
      await Promise.all([
        this.getLeaves(),
        this.getAllPendingSwaps(),
        this.getAllPendingOutgoingTransfers(),
        this.transferService.queryPendingTransfers(),
      ]);

    this.log(
      "sync",
      `Fetched: ${rawLeaves.length} leaves, ${swaps.length} pending swaps, ${outgoingTransfers.length} outgoing, ${incomingTransfers.transfers.length} incoming`,
    );

    const leaves = await this.checkRenewLeaves(rawLeaves);

    await this.leavesMutex.runExclusive(() => {
      const preserved = new Map<string, LeafRecord>();
      for (const [id, record] of this.leaves) {
        if (SYNC_PRESERVED_STATUSES.has(record.status)) {
          preserved.set(id, record);
        }
      }
      if (preserved.size > 0) {
        this.log(
          "sync",
          `Preserving ${preserved.size} in-flight leaves: [${Array.from(
            preserved.entries(),
          )
            .map(([id, r]) => `${id}(${r.status})`)
            .join(",")}]`,
        );
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

      this.hasSynced = true;
    });

    this.log(
      "sync",
      `Complete. Post-sync: ${this.leaves.size} leaves, available=${this.getAvailableBalance()} owned=${this.getOwnedBalance()} incoming=${this.getIncomingBalance()}`,
    );
    this.autoOptimizeIfNeeded();
    this.emitBalanceUpdate();
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

  public async selectLeavesAndExecute<T extends number[], R>(
    targetAmounts: [...T],
    executor: (selectedLeaves: { [K in keyof T]: TreeNode[] }) => Promise<R>,
  ): Promise<R> {
    if (targetAmounts.some((amount) => amount <= 0)) {
      throw new SparkValidationError("Target amount must be positive", {
        field: "targetAmounts",
        value: targetAmounts,
      });
    }

    const totalTargetAmount = targetAmounts.reduce(
      (acc, amount) => acc + amount,
      0,
    );

    // Fast-path check without mutex — the real selection happens under lock in
    // selectLeavesWithSwap, which will fail safely if balance changed.
    const availableBalance = this.getAvailableBalance();
    if (totalTargetAmount > availableBalance) {
      throw new SparkValidationError(
        "Total target amount exceeds available balance",
        {
          field: "targetAmounts",
          value: totalTargetAmount,
          expected: `less than or equal to ${availableBalance}`,
        },
      );
    }

    const executeWithCleanup = async (): Promise<R> => {
      const selectedLeaves = await this.selectLeavesWithSwap(targetAmounts);
      const selectedIds = Object.values(selectedLeaves)
        .flat()
        .map((l) => l.id);

      // Renew any leaves whose timelocks are close to expiry before handing
      // them to the executor. This avoids an avoidable round-trip from the
      // stale-leaf retry path when a leaf expires between sync() calls.
      const allFlat = Object.values(selectedLeaves).flat();
      const renewed = await this.checkRenewLeaves(allFlat);
      if (renewed.length < allFlat.length) {
        const renewedIds = new Set(renewed.map((l) => l.id));
        const dropped = allFlat.filter((l) => !renewedIds.has(l.id));
        this.logger.warn(
          `${dropped.length} leaf(es) dropped during renewal — will cause stale-leaf retry. Leaf IDs: ${dropped
            .map((l) => l.id)
            .join(",")}`,
        );
      }
      if (renewed.length > 0) {
        const renewedMap = new Map(renewed.map((l) => [l.id, l]));
        for (const key of Object.keys(selectedLeaves)) {
          const batch = (selectedLeaves as Record<string, TreeNode[]>)[key]!;
          for (let i = 0; i < batch.length; i++) {
            const fresh = renewedMap.get(batch[i]!.id);
            if (fresh) {
              batch[i] = fresh;
              // Keep cache consistent with what the executor receives
              const record = this.leaves.get(fresh.id);
              if (record) record.treeNode = fresh;
            }
          }
        }
      }

      try {
        const result = await executor(selectedLeaves);
        // Executor succeeded — mark leaves still LOCAL_LOCKED as OUTGOING.
        // Only transition LOCAL_LOCKED leaves — leaves that have already been
        // advanced (OUTGOING/SPENT via handleTransferEvent) or re-claimed
        // (AVAILABLE via registerClaimedLeaves, e.g., self-transfers) must not
        // be touched.
        for (const id of selectedIds) {
          const record = this.leaves.get(id);
          if (record?.status === LeafStatus.LOCAL_LOCKED) {
            record.status = LeafStatus.OUTGOING;
          }
        }
        return result;
      } catch (error) {
        // On failure: restore leaves still LOCAL_LOCKED back to AVAILABLE.
        // If the executor contacted the SO, it should have already advanced
        // the state (e.g., to OUTGOING/SPENT via handleTransferEvent).
        // restoreLocalLockedToAvailable only touches LOCAL_LOCKED, so leaves
        // the executor already advanced are left alone.
        this.restoreLocalLockedToAvailable(selectedIds);
        throw error;
      }
    };

    try {
      return await executeWithCleanup();
    } catch (error) {
      if (this.isStaleLeafError(error)) {
        await this.sync();
        const refreshedBalance = this.getAvailableBalance();
        if (totalTargetAmount > refreshedBalance) {
          throw new SparkValidationError(
            "Total target amount exceeds available balance",
            {
              field: "targetAmounts",
              value: totalTargetAmount,
              expected: `less than or equal to ${refreshedBalance}`,
            },
          );
        }
        return await executeWithCleanup();
      }
      throw error;
    }
  }

  public async addLeaves(leaves: TreeNode[]) {
    let changed = false;
    await this.leavesMutex.runExclusive(() => {
      for (const leaf of leaves) {
        const existing = this.leaves.get(leaf.id);
        if (existing && IN_FLIGHT_STATUSES.has(existing.status)) {
          this.log(
            "addLeaves",
            `Skipping leaf=${leaf.id} value=${leaf.value} — in-flight (${existing.status})`,
          );
          continue;
        }
        // Skip if already AVAILABLE with same value — no change to emit
        if (
          existing?.status === LeafStatus.AVAILABLE &&
          existing.treeNode.value === leaf.value
        )
          continue;
        this.log(
          "addLeaves",
          `Adding leaf=${leaf.id} value=${leaf.value}${existing ? ` (was ${existing.status})` : ""}`,
        );
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.AVAILABLE,
          source: { kind: "none" },
        });
        changed = true;
      }
    });
    if (changed) this.emitBalanceUpdate();
  }

  /** Add leaves as INCOMING (unclaimed transfer or unconfirmed deposit).
   *  Does not overwrite leaves already in the cache with a non-INCOMING status. */
  public async addIncomingLeaves(leaves: TreeNode[], transferId: string) {
    this.log(
      "addIncomingLeaves",
      `Adding ${leaves.length} incoming leaves (${leaves.reduce((a, l) => a + l.value, 0)} sats) transfer=${transferId} ids=[${leaves.map((l) => l.id).join(",")}]`,
    );
    let changed = false;
    await this.leavesMutex.runExclusive(() => {
      for (const leaf of leaves) {
        const existing = this.leaves.get(leaf.id);
        if (existing && existing.status !== LeafStatus.INCOMING) {
          this.log(
            "addIncomingLeaves",
            `Skipping leaf=${leaf.id} — already ${existing.status}`,
          );
          continue;
        }
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.INCOMING,
          source: { kind: "transfer", transferId },
        });
        changed = true;
      }
    });
    if (changed) this.emitBalanceUpdate();
  }

  /** Remove stale AVAILABLE leaves not in the fresh coordinator set.
   *  Only evicts leaves with source "none" — freshly claimed leaves
   *  (source "transfer") are preserved since they may not yet appear
   *  in the coordinator response. */
  public async evictStaleAvailable(freshIds: Set<string>) {
    let changed = false;
    const evicted: string[] = [];
    await this.leavesMutex.runExclusive(() => {
      for (const [id, record] of this.leaves) {
        if (
          record.status === LeafStatus.AVAILABLE &&
          record.source.kind === "none" &&
          !freshIds.has(id)
        ) {
          evicted.push(id);
          this.leaves.delete(id);
          changed = true;
        }
      }
    });
    if (evicted.length > 0) {
      this.log(
        "evictStaleAvailable",
        `Evicted ${evicted.length} stale leaves: [${evicted.join(",")}]`,
      );
    }
    if (changed) this.emitBalanceUpdate();
  }

  public async removeLeaves(leafIds: string[]) {
    this.log(
      "removeLeaves",
      `Removing ${leafIds.length} leaves: [${leafIds.join(",")}]`,
    );
    let changed = false;
    await this.leavesMutex.runExclusive(() => {
      for (const id of leafIds) {
        if (this.leaves.delete(id)) changed = true;
      }
    });
    if (changed) this.emitBalanceUpdate();
  }

  /** Register newly claimed leaves — renews them and adds to cache.
   *  Unconditionally sets status to AVAILABLE, bypassing the IN_FLIGHT_STATUSES
   *  guard in addLeaves, since successfully claimed leaves are definitively ours. */
  public async registerClaimedLeaves(
    leaves: TreeNode[],
    transferId?: string,
  ): Promise<TreeNode[]> {
    this.log(
      "registerClaimedLeaves",
      `Registering ${leaves.length} claimed leaves (${leaves.reduce((a, l) => a + l.value, 0)} sats) transferId=${transferId ?? "none"} ids=[${leaves.map((l) => l.id).join(",")}]`,
    );
    const renewed = await this.checkRenewLeaves(leaves);
    await this.leavesMutex.runExclusive(() => {
      for (const leaf of renewed) {
        const existing = this.leaves.get(leaf.id);
        if (existing) {
          this.log(
            "registerClaimedLeaves",
            `Overwriting leaf ${leaf.id}: ${existing.status} → AVAILABLE`,
          );
        }
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.AVAILABLE,
          // Tag with the transfer ID so handleTransferEvent can detect stale
          // stream events for self-transfers that reuse the same leaf ID.
          source: transferId
            ? { kind: "transfer", transferId }
            : { kind: "none" },
        });
      }
    });
    this.log(
      "registerClaimedLeaves",
      `Post-claim balance: available=${this.getAvailableBalance()} owned=${this.getOwnedBalance()} incoming=${this.getIncomingBalance()}`,
    );
    this.emitBalanceUpdate();
    this.autoOptimizeIfNeeded();
    return renewed;
  }

  /** Select all available leaves and execute an operation with them. */
  public async executeWithAllLeaves<R>(
    executor: (leaves: TreeNode[]) => Promise<R>,
  ): Promise<R> {
    // Lock → capture → unlock → execute → update (same pattern as selectLeavesWithSwap)
    // to avoid holding the mutex during network I/O which could deadlock with
    // stream event handlers that also acquire the mutex.
    const { available, lockedIds } = await this.leavesMutex.runExclusive(() => {
      const available = this.getAvailableLeaves();
      const lockedIds = available.map((l) => l.id);
      this.transition(lockedIds, LeafStatus.LOCAL_LOCKED);
      return { available, lockedIds };
    });

    // Renew leaves with expiring timelocks before passing to the executor.
    const renewed = await this.checkRenewLeaves(available);
    const renewedIds = new Set(renewed.map((l) => l.id));
    if (renewed.length < available.length) {
      // Restore dropped leaves to AVAILABLE — they were never passed to the
      // executor so they must not be marked OUTGOING on success.
      const droppedIds = available
        .filter((l) => !renewedIds.has(l.id))
        .map((l) => l.id);
      this.logger.warn(
        `${droppedIds.length} leaf(es) dropped during renewal. Leaf IDs: ${droppedIds.join(",")}`,
      );
      this.restoreLocalLockedToAvailable(droppedIds);
    }
    // Update cache with renewed tree nodes
    for (const leaf of renewed) {
      const record = this.leaves.get(leaf.id);
      if (record) record.treeNode = leaf;
    }

    try {
      const result = await executor(renewed);
      // Only transition leaves that were actually passed to the executor
      for (const id of lockedIds) {
        if (!renewedIds.has(id)) continue;
        const record = this.leaves.get(id);
        if (record?.status === LeafStatus.LOCAL_LOCKED) {
          record.status = LeafStatus.OUTGOING;
        }
      }
      return result;
    } catch (error) {
      // Only restore leaves that were actually passed to the executor.
      // Dropped leaves were already restored before the executor ran.
      const renewedLockedIds = lockedIds.filter((id) => renewedIds.has(id));
      this.restoreLocalLockedToAvailable(renewedLockedIds);
      throw error;
    }
  }

  /** Returns true if the deposit was added/updated in the cache. */
  public async handleDepositEvent(deposit: TreeNode): Promise<boolean> {
    this.log(
      "handleDepositEvent",
      `deposit=${deposit.id} status=${deposit.status} value=${deposit.value}`,
    );
    let needsVerification = false;
    let added = false;

    let changed = false;
    await this.leavesMutex.runExclusive(() => {
      if (deposit.status === "CREATING") {
        const existing = this.leaves.get(deposit.id);
        if (!existing) {
          this.log(
            "handleDepositEvent",
            `leaf=${deposit.id} CREATING → INCOMING`,
          );
          this.leaves.set(deposit.id, {
            treeNode: deposit,
            status: LeafStatus.INCOMING,
            source: { kind: "deposit", depositId: deposit.id },
          });
          changed = true;
          added = true;
        } else {
          this.log(
            "handleDepositEvent",
            `leaf=${deposit.id} CREATING — already in cache (${existing.status}), skipped`,
          );
        }
      } else if (deposit.status === "AVAILABLE") {
        const existing = this.leaves.get(deposit.id);
        if (existing) {
          if (
            !IN_FLIGHT_STATUSES.has(existing.status) &&
            existing.status !== LeafStatus.AVAILABLE
          ) {
            this.log(
              "handleDepositEvent",
              `leaf=${deposit.id} ${existing.status} → AVAILABLE`,
            );
            existing.treeNode = deposit;
            this.transition([deposit.id], LeafStatus.AVAILABLE);
            changed = true;
            added = true;
          } else {
            this.log(
              "handleDepositEvent",
              `leaf=${deposit.id} already ${existing.status}, skipped`,
            );
          }
        } else if (!this.hasSynced) {
          this.log(
            "handleDepositEvent",
            `leaf=${deposit.id} pre-sync → AVAILABLE`,
          );
          this.leaves.set(deposit.id, {
            treeNode: deposit,
            status: LeafStatus.AVAILABLE,
            source: { kind: "deposit", depositId: deposit.id },
          });
          changed = true;
          added = true;
        } else {
          needsVerification = true;
        }
      }
    });
    if (changed) this.emitBalanceUpdate();

    if (needsVerification) {
      this.log(
        "handleDepositEvent",
        `Deposit ${deposit.id} needs verification (post-sync unknown leaf)`,
      );
      added = await this.verifyAndAddLeaf(deposit.id);
    }

    this.log(
      "handleDepositEvent",
      `Deposit ${deposit.id} result: added=${added} balance: available=${this.getAvailableBalance()} owned=${this.getOwnedBalance()}`,
    );
    return added;
  }

  /** Query the coordinator for a specific leaf and add it if it's AVAILABLE
   *  and owned by us. Used to validate stream events for unknown leaves.
   *  Returns true if the leaf was verified and added. */
  private async verifyAndAddLeaf(leafId: string): Promise<boolean> {
    try {
      const response = await this.queryNodes({
        source: { $case: "nodeIds", nodeIds: { nodeIds: [leafId] } },
        includeParents: false,
        network: this.config.getNetworkProto(),
        statuses: [],
      });

      const node = response.nodes[leafId];
      if (!node || node.status !== "AVAILABLE") return false;

      let wasAdded = false;
      await this.leavesMutex.runExclusive(() => {
        const existing = this.leaves.get(leafId);
        // Already AVAILABLE — treat as successfully verified (may have been
        // added by a concurrent sync()), so DepositConfirmed still fires.
        if (existing?.status === LeafStatus.AVAILABLE) {
          wasAdded = true;
          return;
        }
        if (existing) return; // in-flight — don't overwrite

        this.leaves.set(leafId, {
          treeNode: node,
          status: LeafStatus.AVAILABLE,
          source: { kind: "deposit", depositId: leafId },
        });
        wasAdded = true;
      });
      if (wasAdded) this.emitBalanceUpdate();
      return wasAdded;
    } catch {
      return false;
    }
  }

  public async handleTransferEvent(transfer: Transfer) {
    if (
      !this.identityPublicKey ||
      !equalBytes(transfer.senderIdentityPublicKey, this.identityPublicKey)
    ) {
      return;
    }
    const leafIds = transfer.leaves.flatMap((leaf) =>
      leaf.leaf ? [leaf.leaf.id] : [],
    );

    this.log(
      "handleTransferEvent",
      `transfer=${transfer.id} type=${transfer.type} status=${transfer.status} leaves=[${leafIds.join(",")}]`,
    );

    let changed = false;
    await this.leavesMutex.runExclusive(() => {
      const source: LeafSource = { kind: "transfer", transferId: transfer.id };

      // Skip leaves that were already reclaimed for this transfer (self-transfer).
      // These are AVAILABLE with source matching this transfer ID — stale stream
      // events must not re-transition them to OUTGOING/SPENT.
      const activeLeafIds = leafIds.filter((id) => {
        const record = this.leaves.get(id);
        if (!record) return true; // not in cache — let transition handle it
        if (record.status !== LeafStatus.AVAILABLE) return true; // in-flight — proceed
        // AVAILABLE leaf: skip if its source is this same transfer (reclaimed)
        return !(
          record.source.kind === "transfer" &&
          record.source.transferId === transfer.id
        );
      });

      switch (transfer.status) {
        case TransferStatus.TRANSFER_STATUS_RETURNED:
        case TransferStatus.TRANSFER_STATUS_EXPIRED:
          this.log(
            "handleTransferEvent",
            `Returned/expired → restoring ${activeLeafIds.length} leaves to AVAILABLE`,
          );
          this.transition(activeLeafIds, LeafStatus.AVAILABLE, {
            source: { kind: "none" },
          });
          changed = true;
          break;
        case TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING:
        case TransferStatus.TRANSFER_STATUS_SENDER_INITIATED:
        case TransferStatus.TRANSFER_STATUS_SENDER_INITIATED_COORDINATOR:
        case TransferStatus.TRANSFER_STATUS_APPLYING_SENDER_KEY_TWEAK: {
          const isSwap =
            transfer.type === TransferType.PRIMARY_SWAP_V3 ||
            transfer.type === TransferType.SWAP;
          const targetStatus = isSwap
            ? LeafStatus.SWAP_PENDING
            : LeafStatus.OUTGOING;
          this.log(
            "handleTransferEvent",
            `Sender initiated → ${targetStatus} for ${activeLeafIds.length} leaves (isSwap=${isSwap}), current statuses: [${activeLeafIds
              .map((id) => {
                const r = this.leaves.get(id);
                return r ? `${id}(${r.status})` : `${id}(missing)`;
              })
              .join(",")}]`,
          );
          this.transition(activeLeafIds, targetStatus, { source });
          changed = true;
          break;
        }
        default:
          if (transfer.status !== TransferStatus.UNRECOGNIZED) {
            // Skip SWAP_PENDING leaves — their lifecycle is managed by the
            // swap/optimization path which transitions them to SPENT and adds
            // replacement leaves atomically. Letting the stream pre-empt this
            // would delete the leaf before replacements are added, causing a
            // temporary balance drop.

            const nonSwapIds = activeLeafIds.filter((id) => {
              const record = this.leaves.get(id);
              return !record || record.source.kind !== "swap";
            });
            const skippedSwapIds = activeLeafIds.filter(
              (id) => !nonSwapIds.includes(id),
            );
            if (skippedSwapIds.length > 0) {
              this.log(
                "handleTransferEvent",
                `Terminal status=${transfer.status} — skipping ${skippedSwapIds.length} SWAP_PENDING leaves: [${skippedSwapIds.join(",")}]`,
              );
            }
            this.log(
              "handleTransferEvent",
              `Terminal status=${transfer.status} → SPENT for ${nonSwapIds.length} leaves: [${nonSwapIds.join(",")}]`,
            );
            this.transition(nonSwapIds, LeafStatus.SPENT, { source });
            changed = true;
          }
          break;
      }
    });
    if (changed) {
      this.log(
        "handleTransferEvent",
        `Post-event balance: available=${this.getAvailableBalance()} owned=${this.getOwnedBalance()} incoming=${this.getIncomingBalance()}`,
      );
      this.emitBalanceUpdate();
    }
  }

  // ---------------------------------------------------------------------------
  // Balance
  // ---------------------------------------------------------------------------

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

  private getAvailableLeaves(): TreeNode[] {
    return this.getLeavesByStatus(LeafStatus.AVAILABLE);
  }

  public isOptimizing(): boolean {
    return this.optimizationInProgress;
  }

  /** Read-only leaf selection for queries (fee quotes, etc). Does NOT lock leaves. */
  public selectLeavesReadOnly(targetAmount: number): TreeNode[] {
    const sorted = [...this.getAvailableLeaves()].sort(
      (a, b) => b.value - a.value,
    );
    const selected: TreeNode[] = [];
    let amount = 0;
    for (const leaf of sorted) {
      if (amount >= targetAmount) break;
      amount += leaf.value;
      selected.push(leaf);
    }
    return selected;
  }

  // ---------------------------------------------------------------------------
  // Leaf Selection
  // ---------------------------------------------------------------------------

  private async selectLeavesWithSwap<T extends number[]>(
    targetAmounts: [...T],
  ): Promise<{ [K in keyof T]: TreeNode[] }> {
    let lockedForSwap: TreeNode[] | undefined;

    // Phase 1: Try exact selection under lock
    const release = await this.leavesMutex.acquire();
    try {
      const [results, found] = this.selectLeaves(targetAmounts);
      if (found) {
        const allSelected = Object.values(results).flat();
        this.transition(
          allSelected.map((l) => l.id),
          LeafStatus.LOCAL_LOCKED,
        );
        return results;
      }

      // Phase 2: Need a swap — lock leaves, capture IDs, then release for the network call
      const totalTargetAmount = targetAmounts.reduce((acc, a) => acc + a, 0);
      lockedForSwap = this.determineLeavesToSwap(totalTargetAmount);
      this.transition(
        lockedForSwap.map((l) => l.id),
        LeafStatus.LOCAL_LOCKED,
      );
    } finally {
      release();
    }

    // Phase 3: Execute swap outside lock — use captured leaves, NOT getLeavesByStatus
    const swapLeafIds = lockedForSwap!.map((l) => l.id);
    let newLeaves: TreeNode[];
    try {
      newLeaves = await this.swapService.requestLeavesSwap({
        leaves: lockedForSwap!,
        targetAmounts,
        onSwapInitiated: async () => {
          await this.leavesMutex.runExclusive(() => {
            this.transition(swapLeafIds, LeafStatus.SWAP_PENDING);
          });
        },
        registerSwapTransferId: (transferId) => {
          this.updateLeavesSource(swapLeafIds, {
            kind: "swap",
            swapId: transferId,
          });
        },
      });
    } catch (error) {
      // Only restore LOCAL_LOCKED leaves — if onSwapInitiated fired, the leaves
      // are SWAP_PENDING and the SO has them locked. Those will be reconciled
      // on the next sync(). LOCAL_LOCKED means the SO was never contacted.
      this.restoreLocalLockedToAvailable(swapLeafIds);
      throw error;
    }

    // Renew SSP-returned leaves before caching. The SSP may send leaves
    // with low timelocks (≤200) that would crash the next swap signing
    // path (signRefundsCore: "timelock interval is less than or equal to 0").
    try {
      const preRenewalLeaves = newLeaves;
      const renewedLeaves = await this.checkRenewLeaves(preRenewalLeaves);
      if (renewedLeaves.length === preRenewalLeaves.length) {
        newLeaves = renewedLeaves;
      } else {
        // Partial renewal — one or more nodes were silently dropped. Cache
        // the originals so Phase 4 can still select the correct amounts;
        // low-timelock leaves will be renewed on next sync() or access.
        this.logger.warn(
          `checkRenewLeaves returned fewer leaves after swap, caching originals. before=${preRenewalLeaves.length} after=${renewedLeaves.length}`,
        );
      }
    } catch (err) {
      // Renewal failed (e.g. network error). Cache the original leaves so
      // Phase 4 can still update state and preserve the user's balance.
      // Low-timelock leaves will be renewed on the next sync() or access.
      this.logger.warn(
        `checkRenewLeaves failed after swap, caching original leaves. Error: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
    }

    // Phase 4: Update state and re-select under lock
    return await this.leavesMutex.runExclusive(() => {
      this.transition(swapLeafIds, LeafStatus.SPENT);
      for (const leaf of newLeaves) {
        this.leaves.set(leaf.id, {
          treeNode: leaf,
          status: LeafStatus.AVAILABLE,
          source: { kind: "none" },
        });
      }

      const [newResults, newFound] = this.selectLeaves(targetAmounts);
      if (!newFound) {
        // Cache was mutated (old leaves spent, new leaves added) — notify
        // subscribers even though re-selection failed.
        this.emitBalanceUpdate();
        throw new Error(
          "Failed to select leaves for the target amounts after swap",
        );
      }
      const allSelected = Object.values(newResults).flat();
      this.transition(
        allSelected.map((l) => l.id),
        LeafStatus.LOCAL_LOCKED,
      );
      return newResults;
    });
  }

  /**
   * Greedy exact-fit selection. Returns [batches, success].
   * Must be called while holding the mutex.
   */
  private selectLeaves<T extends number[]>(
    targetAmounts: [...T],
  ): [{ [K in keyof T]: TreeNode[] }, boolean] {
    const availableLeaves = this.getAvailableLeaves();
    const sorted = [...availableLeaves].sort((a, b) => b.value - a.value);

    // Process targets ascending — smaller targets have fewer valid leaf
    // combinations and should claim leaves first to avoid the greedy
    // algorithm missing valid exact-fit solutions.
    const indexed = targetAmounts.map((amount, i) => ({ amount, i }));
    indexed.sort((a, b) => a.amount - b.amount);

    const usedIds = new Set<string>();
    const batches: TreeNode[][] = new Array(targetAmounts.length);
    let totalAmount = 0;

    for (const { amount: targetAmount, i: originalIndex } of indexed) {
      const nodes: TreeNode[] = [];
      let amount = 0;

      for (const leaf of sorted) {
        if (usedIds.has(leaf.id)) continue;
        if (targetAmount - amount >= leaf.value) {
          amount += leaf.value;
          nodes.push(leaf);
          usedIds.add(leaf.id);
        }
      }

      totalAmount += amount;
      batches[originalIndex] = nodes;
    }

    const results = {} as { [K in keyof T]: TreeNode[] };
    for (let i = 0; i < targetAmounts.length; i++) {
      results[i] = batches[i] ?? [];
    }

    const totalTargetAmount = targetAmounts.reduce((acc, a) => acc + a, 0);
    return [results, totalAmount === totalTargetAmount];
  }

  /** Must be called while holding the mutex. */
  private determineLeavesToSwap(targetAmount: number): TreeNode[] {
    const sorted = [...this.getAvailableLeaves()].sort(
      (a, b) => a.value - b.value,
    );
    let amount = 0;
    const nodes: TreeNode[] = [];
    for (const leaf of sorted) {
      if (amount >= targetAmount) break;
      amount += leaf.value;
      nodes.push(leaf);
    }
    if (amount < targetAmount) {
      throw new Error("Not enough leaves to swap for the target amount");
    }
    return nodes;
  }

  // ---------------------------------------------------------------------------
  // Optimization
  // ---------------------------------------------------------------------------

  private logOptimizeLeaves(message: string) {
    this.log("optimizeLeaves", message);
  }

  private async autoOptimizeIfNeeded(): Promise<void> {
    try {
      if (!this.config.getOptimizationOptions().auto) return;
      const available = this.getLeavesByStatus(LeafStatus.AVAILABLE);
      if (
        !shouldOptimize(
          available.map((l) => l.value),
          this.config.getOptimizationOptions().multiplicity ?? 0,
        )
      ) {
        this.log(
          "autoOptimizeIfNeeded",
          `No optimization needed for ${available.length} leaves`,
        );
        return;
      }

      if (!this.onAutoOptimize) return;
      this.log(
        "autoOptimizeIfNeeded",
        `Optimizing leaves for ${available.length} leaves`,
      );
      await this.onAutoOptimize();
    } catch {
      // Optimization is best-effort. If it fails (e.g., config error, another
      // instance already locked the leaf, or SSP is unavailable), the leaves
      // remain AVAILABLE.
    }
  }

  public async *optimizeLeaves(
    multiplicity: number | undefined = undefined,
  ): AsyncGenerator<
    { step: number; total: number; controller: AbortController },
    void,
    void
  > {
    const multiplicityValue =
      multiplicity ?? this.config.getOptimizationOptions().multiplicity ?? 0;
    if (multiplicityValue < 0) {
      throw new SparkValidationError("Multiplicity cannot be negative");
    } else if (multiplicityValue > 5) {
      throw new SparkValidationError("Multiplicity cannot be greater than 5");
    }

    this.logOptimizeLeaves(
      `Starting optimization with multiplicity ${multiplicityValue}`,
    );
    if (this.optimizationInProgress) {
      this.logOptimizeLeaves(`Optimization already in progress`);
      return;
    }

    const controller = new AbortController();
    let ownsFlag = false;
    let swapBatches: { leavesToSend: TreeNode[]; outLeaves: number[] }[] = [];
    let outerRelease: (() => void) | undefined =
      await this.leavesMutex.acquire();
    try {
      // Second check under lock — guards against TOCTOU where two callers
      // both pass the optimistic check before either acquires the mutex.
      if (this.optimizationInProgress) {
        this.logOptimizeLeaves(
          `Second check under lock — Optimization already in progress`,
        );
        return;
      }
      this.optimizationInProgress = true;
      ownsFlag = true;

      const availableLeaves = this.getAvailableLeaves();
      const swaps = optimize(
        availableLeaves.map((leaf) => leaf.value),
        multiplicityValue,
      );
      if (swaps.length === 0) {
        this.logOptimizeLeaves(
          `No swaps needed for ${availableLeaves.length} leaves`,
        );
        return;
      }

      this.logOptimizeLeaves(
        `Planned ${swaps.length} swap(s): ${JSON.stringify(swaps.map((s) => ({ in: s.inLeaves, out: s.outLeaves })))}`,
      );

      const valueToNodes = new Map<number, TreeNode[]>();
      for (const leaf of availableLeaves) {
        let bucket = valueToNodes.get(leaf.value);
        if (!bucket) {
          bucket = [];
          valueToNodes.set(leaf.value, bucket);
        }
        bucket.push(leaf);
      }

      swapBatches = [];
      for (const swap of swaps) {
        const leavesToSend: TreeNode[] = [];
        for (const leafValue of swap.inLeaves) {
          const nodes = valueToNodes.get(leafValue);
          if (nodes && nodes.length > 0) {
            leavesToSend.push(nodes.shift()!);
          }
        }
        swapBatches.push({ leavesToSend, outLeaves: swap.outLeaves });
        this.logOptimizeLeaves(
          `Batch ${swapBatches.length}: LOCAL_LOCKED ${leavesToSend.length} leaves (${leavesToSend.reduce((acc, leaf) => acc + leaf.value, 0)} sats) ids=[${leavesToSend.map((l) => l.id).join(",")}]`,
        );
        this.transition(
          leavesToSend.map((l) => l.id),
          LeafStatus.LOCAL_LOCKED,
        );
      }
      outerRelease();
      outerRelease = undefined;

      // Yield step 0 after releasing the mutex so consumers can do async work
      // (e.g., UI updates that call addLeaves/sync) without deadlocking.
      yield { step: 0, total: swapBatches.length, controller };

      for (let i = 0; i < swapBatches.length; i++) {
        const swap = swapBatches[i]!;
        if (controller.signal.aborted) break;

        const swapLeafIds = swap.leavesToSend.map((l) => l.id);
        const totalValue = swap.leavesToSend.reduce(
          (acc, leaf) => acc + leaf.value,
          0,
        );
        try {
          this.logOptimizeLeaves(
            `Requesting swap ${i + 1} of ${swapBatches.length}: ${totalValue} sats, ids=[${swapLeafIds.join(",")}] -> [${swap.outLeaves.join(",")}]`,
          );
          const newLeaves = await this.swapService.requestLeavesSwap({
            leaves: swap.leavesToSend,
            targetAmounts: swap.outLeaves,
            onSwapInitiated: async () => {
              this.logOptimizeLeaves(
                `Swap ${i + 1} initiated. Transitioning leaves to SWAP_PENDING: ${totalValue} sats`,
              );
              await this.leavesMutex.runExclusive(() => {
                this.transition(swapLeafIds, LeafStatus.SWAP_PENDING);
              });
            },
            registerSwapTransferId: (transferId) => {
              this.updateLeavesSource(swapLeafIds, {
                kind: "swap",
                swapId: transferId,
              });
            },
          });

          await this.leavesMutex.runExclusive(() => {
            this.logOptimizeLeaves(
              `Swap ${i + 1} completed. SPENT ${totalValue} sats ids=[${swapLeafIds.join(",")}], received ${newLeaves.length} leaves (${newLeaves.reduce((acc, leaf) => acc + leaf.value, 0)} sats) ids=[${newLeaves.map((l) => l.id).join(",")}]`,
            );
            this.transition(swapLeafIds, LeafStatus.SPENT);
            for (const leaf of newLeaves) {
              this.leaves.set(leaf.id, {
                treeNode: leaf,
                status: LeafStatus.AVAILABLE,
                source: { kind: "none" },
              });
            }
            this.emitBalanceUpdate();
            this.logOptimizeLeaves(
              `Post-swap balance: available=${this.getAvailableBalance()} owned=${this.getOwnedBalance()}`,
            );
          });
        } catch (error) {
          this.logOptimizeLeaves(
            `Error requesting swap ${i + 1} of ${swapBatches.length}: ${error}. Restoring ids=[${swapLeafIds.join(",")}]`,
          );
          // Only restore LOCAL_LOCKED leaves — SWAP_PENDING means the SO was
          // contacted and has them locked; sync() will reconcile those.
          this.restoreLocalLockedToAvailable(swapLeafIds);
          // Restore all remaining unprocessed batches (always LOCAL_LOCKED)
          for (let j = i + 1; j < swapBatches.length; j++) {
            const remainingIds = swapBatches[j]!.leavesToSend.map((l) => l.id);
            this.logOptimizeLeaves(
              `Restoring remaining batch ${j + 1} ids=[${remainingIds.join(",")}]`,
            );
            this.restoreLocalLockedToAvailable(remainingIds);
          }
          // emitBalanceUpdate deferred to finally block
          throw error;
        }

        yield { step: i + 1, total: swapBatches.length, controller };
      }
    } finally {
      if (ownsFlag) {
        this.optimizationInProgress = false;
        // Restore any LOCAL_LOCKED leaves that were never processed — covers
        // abort, consumer break, and early return. restoreLocalLockedToAvailable
        // is idempotent (no-op for non-LOCAL_LOCKED leaves).
        if (swapBatches.length > 0) {
          for (const swap of swapBatches) {
            const ids = swap.leavesToSend.map((l) => l.id);
            this.logOptimizeLeaves(
              `Cleanup: restoring batch ids=[${ids.join(",")}]`,
            );
            this.restoreLocalLockedToAvailable(ids);
          }
          this.emitBalanceUpdate();
        }
        this.logOptimizeLeaves(
          `Optimization complete. Final balance: available=${this.getAvailableBalance()} owned=${this.getOwnedBalance()}`,
        );
      }
      outerRelease?.();
    }
  }

  // ---------------------------------------------------------------------------
  // State Machine
  // ---------------------------------------------------------------------------

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

  private updateLeavesSource(leafIds: string[], meta: LeafSource): void {
    for (const leafId of leafIds) {
      const leaf = this.leaves.get(leafId);
      if (!leaf || leaf.status === LeafStatus.AVAILABLE) {
        continue;
      }
      leaf.source = meta;
    }
  }

  // ---------------------------------------------------------------------------
  // Leaf Renewal
  // ---------------------------------------------------------------------------

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
        this.logger.warn(
          `checkRenewLeaves validation failed for node ${node.id}. Error: ${
            err instanceof Error ? err.message : String(err)
          }`,
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
          this.logger.warn(
            `renewNodeTxn failed for node ${node.id}. Error: ${
              err instanceof Error ? err.message : String(err)
            }`,
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
          this.logger.warn(
            `renewRefundTxn failed for node ${node.id}. Error: ${
              err instanceof Error ? err.message : String(err)
            }`,
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
          this.logger.warn(
            `renewZeroTimelockNodeTxn failed for node ${node.id}. Error: ${
              err instanceof Error ? err.message : String(err)
            }`,
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

  // ---------------------------------------------------------------------------
  // Network Queries
  // ---------------------------------------------------------------------------

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

  // ---------------------------------------------------------------------------
  // Recovery
  // ---------------------------------------------------------------------------

  private async recoverLeaves(
    leaves: TreeNode[],
    keyDerivation: KeyDerivation,
  ): Promise<TreeNode[]> {
    const selfIdentityPubkey = await this.config.signer.getIdentityPublicKey();
    const leafKeyTweaks: LeafKeyTweak[] = leaves.map((leaf) => ({
      leaf,
      keyDerivation,
      newKeyDerivation: { type: KeyDerivationType.RANDOM },
      receiverIdentityPublicKey: selfIdentityPubkey,
    }));

    const transfer =
      await this.transferService.sendTransferWithKeyTweaks(leafKeyTweaks);

    const pendingTransfer = await this.transferService.queryTransfer(
      transfer.id,
    );
    return pendingTransfer
      ? await this.transferService.claimTransfer(pendingTransfer)
      : [];
  }

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

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

  /**
   * Restore leaves that are still LOCAL_LOCKED back to AVAILABLE.
   * Safe to call after an executor returns — if the SO was successfully contacted,
   * the status would have already changed to OUTGOING/SWAP_PENDING.
   *
   * Public so that batch-send callers can release leaves for failed jobs
   * before the executor returns (preventing the post-executor transition
   * from incorrectly marking them OUTGOING).
   */
  public restoreLocalLockedToAvailable(leafIds: string[]): void {
    let changed = false;
    for (const id of leafIds) {
      const record = this.leaves.get(id);
      if (record?.status === LeafStatus.LOCAL_LOCKED) {
        record.status = LeafStatus.AVAILABLE;
        record.source = { kind: "none" };
        changed = true;
      }
    }
    if (changed) this.emitBalanceUpdate();
  }

  /**
   * Detects SO errors that indicate our cached leaf state is stale.
   * This covers: leaf locked by another instance, leaf ownership changed
   * after a swap by another instance, or leaf otherwise unavailable.
   */
  private isStaleLeafError(error: unknown): boolean {
    if (!(error instanceof Error)) return false;
    const msg = error.message.toLowerCase();
    return (
      msg.includes("not available to transfer") ||
      msg.includes("not owned by") ||
      msg.includes("leaf is unavailable") ||
      msg.includes("leaf is not available")
    );
  }

  private getLeavesByStatus(status: LeafStatus): TreeNode[] {
    const result: TreeNode[] = [];
    for (const record of this.leaves.values()) {
      if (record.status === status) result.push(record.treeNode);
    }
    return result;
  }
}
