import { equalBytes } from "@noble/curves/utils";
import { Mutex } from "async-mutex";
import {
  SparkValidationError,
  addPublicKeys,
  doesTxnNeedRenewed,
  getTxFromRawTxBytes,
  isZeroTimelock,
} from "../index-shared.js";
import {
  QueryNodesRequest,
  QueryNodesResponse,
  TreeNode,
  TreeNodeStatus,
} from "../proto/spark.js";
import { KeyDerivation, KeyDerivationType } from "../signer/types.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./index.js";
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

export default class LeafManager {
  private leaves: Map<string, LeafRecord> = new Map();

  private leavesMutex = new Mutex();

  constructor(
    private readonly config: WalletConfigService,
    private readonly swapService: SwapService,
    private readonly transferService: TransferService,
    private readonly connectionManager: ConnectionManager,
  ) {}

  // #region Public API
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
  // #endregion

  // #region Leaf Renewal
  private async checkRenewLeaves(nodes: TreeNode[]): Promise<TreeNode[]> {
    const nodesToRenewNodeTxn: TreeNode[] = [];
    const nodesToRenewRefundTxn: TreeNode[] = [];
    const nodesToRenewZeroTimelockTxn: TreeNode[] = [];
    const nodeIds: string[] = [];
    const validNodes: TreeNode[] = [];

    for (const node of nodes) {
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
