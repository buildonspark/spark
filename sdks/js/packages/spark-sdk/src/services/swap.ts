import { bytesToHex, hexToBytes } from "@lightsparkdev/core";
import { SparkValidationError } from "../errors/index.js";
import SspClient from "../graphql/client.js";
import { TreeNode } from "../proto/spark.js";
import { KeyDerivationType } from "../signer/types.js";
import { SparkLeavesSwapRequestStatus, UserLeafInput } from "../types/index.js";
import { chunkArray } from "../utils/chunkArray.js";
import { WalletConfigService } from "./config.js";
import { LeafKeyTweak, TransferService } from "./transfer.js";

const MAX_BATCH_SIZE = 64;

type RequestLeavesSwapParams = {
  leaves: TreeNode[];
  targetAmounts: number[];
  onSwapInitiated?: (leafIds: string[]) => void | Promise<void>;
};

export default class SwapService {
  constructor(
    private readonly config: WalletConfigService,
    private readonly transferService: TransferService,
    private readonly sspClient: SspClient,
  ) {}

  public async requestLeavesSwap({
    leaves,
    targetAmounts,
    onSwapInitiated,
  }: RequestLeavesSwapParams): Promise<TreeNode[]> {
    this.validateSwapInputs(leaves, targetAmounts);

    if (leaves.length <= MAX_BATCH_SIZE) {
      return await this.executeSingleSwap(
        leaves,
        targetAmounts,
        onSwapInitiated,
      );
    }

    const sortedLeaves = [...leaves].sort((a, b) => b.value - a.value);
    const leafBatches = chunkArray(sortedLeaves, MAX_BATCH_SIZE);

    const targetAmountsByBatch = this.distributeTargetAmounts(
      targetAmounts,
      leafBatches,
    );

    const results: TreeNode[] = [];
    for (let i = 0; i < leafBatches.length; i++) {
      const batch = leafBatches[i];
      const batchTargetAmounts = targetAmountsByBatch[i];

      if (
        batch === undefined ||
        batchTargetAmounts === undefined ||
        batchTargetAmounts.length === 0
      ) {
        continue;
      }

      const result = await this.executeSingleSwap(
        batch,
        batchTargetAmounts,
        onSwapInitiated,
      );
      results.push(...result);
    }
    return results;
  }

  private distributeTargetAmounts(
    targetAmounts: number[],
    leafBatches: TreeNode[][],
  ) {
    const targetAmountsByBatch: number[][] = leafBatches.map(() => []);
    const remainingBatchAmounts: number[] = leafBatches.map((batch) =>
      batch.reduce((acc, leaf) => acc + leaf.value, 0),
    );
    const remainingTargets = [...targetAmounts].sort((a, b) => b - a);

    for (const target of remainingTargets) {
      let assigned = false;
      for (let i = 0; i < leafBatches.length; i++) {
        const batchRemaining = remainingBatchAmounts[i];
        const batchTargets = targetAmountsByBatch[i];

        if (
          batchRemaining !== undefined &&
          batchTargets !== undefined &&
          batchRemaining >= target
        ) {
          batchTargets.push(target);
          remainingBatchAmounts[i]! -= target;
          assigned = true;
          break;
        }
      }

      if (!assigned) {
        throw new SparkValidationError(
          `Target amount ${target} could not be assigned to any batch`,
          {
            field: "targetAmounts",
            value: targetAmounts,
            expected: `a batch with at least ${target} remaining capacity`,
          },
        );
      }
    }

    return targetAmountsByBatch;
  }

  private async executeSingleSwap(
    leaves: TreeNode[],
    targetAmounts: number[],
    onSwapInitiated?: (leafIds: string[]) => void | Promise<void>,
  ): Promise<TreeNode[]> {
    this.validateSwapInputs(leaves, targetAmounts);

    const leafKeyTweaks: LeafKeyTweak[] = leaves.map((leaf) => ({
      leaf,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leaf.id,
      },
      newKeyDerivation: {
        type: KeyDerivationType.RANDOM,
      },
    }));

    const { swapTransfer, adaptorPubkey, adaptorAddedSignatureMap } =
      await this.transferService.sendSwapTransfer(
        leafKeyTweaks,
        hexToBytes(this.config.getSspIdentityPublicKey()),
      );

    await onSwapInitiated?.(leaves.map((leaf) => leaf.id));

    if (!swapTransfer.transfer) {
      throw new SparkValidationError("Transfer is missing in swap response", {
        field: "transfer",
        value: swapTransfer.transfer,
        expected: "not null",
      });
    }
    if (swapTransfer.transfer.leaves.some((leaf) => !leaf.leaf)) {
      throw new SparkValidationError("Leaf is missing in swap response", {
        field: "leaves",
        value: swapTransfer.transfer.leaves,
        expected: "not null",
      });
    }

    const transfer = swapTransfer.transfer;

    try {
      const userLeaves: UserLeafInput[] = [];
      for (let i = 0; i < transfer.leaves.length; i++) {
        const leaf = transfer.leaves[i];
        if (!leaf?.leaf) {
          console.error(`[processSwapBatch] Leaf ${i + 1} is missing`);
          throw new Error("Failed to get leaf");
        }

        const adaptorAddedSignature = adaptorAddedSignatureMap.get(
          leaf.leaf.id,
        );

        if (!adaptorAddedSignature) {
          throw new Error("Adaptor added signature not found");
        }
        userLeaves.push({
          leaf_id: leaf.leaf.id,
          raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateRefundTx,
          ),
          direct_raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateDirectRefundTx,
          ),
          direct_from_cpfp_raw_unsigned_refund_transaction: bytesToHex(
            leaf.intermediateDirectFromCpfpRefundTx,
          ),
          adaptor_added_signature: bytesToHex(adaptorAddedSignature),
          direct_adaptor_added_signature: bytesToHex(adaptorAddedSignature),
          direct_from_cpfp_adaptor_added_signature: bytesToHex(
            adaptorAddedSignature,
          ),
        });
      }

      const request = await this.sspClient.requestLeavesSwap({
        userLeaves,
        adaptorPubkey: bytesToHex(adaptorPubkey),
        targetAmountSats: targetAmounts,
        totalAmountSats: leaves.reduce((acc, leaf) => acc + leaf.value, 0),
        // TODO: Request fee from SSP
        feeSats: 0,
        userOutboundTransferExternalId: transfer.id,
      });

      if (
        !request ||
        !request.swapLeaves ||
        request.swapLeaves.length === 0 ||
        request.status === SparkLeavesSwapRequestStatus.FAILED ||
        !request.inboundTransfer?.sparkId
      ) {
        console.error("[processSwapBatch] Leave swap request returned null");
        throw new Error("Failed to request leaves swap. Request failed.");
      }

      const incomingTransfer = await this.transferService.queryTransfer(
        request.inboundTransfer.sparkId,
      );

      if (!incomingTransfer) {
        console.error("[processSwapBatch] No incoming transfer found");
        throw new Error("Failed to get incoming transfer");
      }

      return await this.transferService.claimTransfer(incomingTransfer);
    } catch (e) {
      console.error("[processSwapBatch] Error details:", {
        error: e,
        message: (e as Error).message,
        stack: (e as Error).stack,
      });
      throw new Error(`Failed to request leaves swap: ${e}`, { cause: e });
    }
  }

  private validateSwapInputs(
    leaves: TreeNode[],
    targetAmounts: number[],
  ): void {
    if (targetAmounts.length === 0) {
      throw new SparkValidationError("Target amounts must be non-empty", {
        field: "targetAmounts",
        value: targetAmounts,
      });
    }

    const totalTargetAmount = targetAmounts.reduce(
      (acc, amount) => acc + amount,
      0,
    );

    const totalLeavesValue = leaves.reduce((acc, leaf) => acc + leaf.value, 0);

    if (totalTargetAmount > totalLeavesValue) {
      throw new SparkValidationError(
        "Total target amount exceeds leaves value",
        {
          field: "targetAmounts",
          value: totalTargetAmount,
          expected: `less than or equal to ${totalLeavesValue}`,
        },
      );
    }

    if (targetAmounts && targetAmounts.some((amount) => amount <= 0)) {
      throw new SparkValidationError(
        "specified targetAmount must be positive",
        {
          field: "targetAmounts",
          value: targetAmounts,
          expected: "positive",
        },
      );
    }

    if (targetAmounts.some((amount) => !Number.isSafeInteger(amount))) {
      throw new SparkValidationError("targetAmount must be less than 2^53", {
        field: "targetAmounts",
        value: targetAmounts,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    if (leaves.length > MAX_BATCH_SIZE) {
      const sortedLeaves = [...leaves].sort((a, b) => b.value - a.value);
      const maxBatchCapacity = sortedLeaves
        .slice(0, MAX_BATCH_SIZE)
        .reduce((acc, leaf) => acc + leaf.value, 0);

      const maxTarget = Math.max(...targetAmounts);

      if (maxTarget > maxBatchCapacity) {
        throw new SparkValidationError(
          `Target amount ${maxTarget} exceeds maximum batch capacity ${maxBatchCapacity}`,
          {
            field: "targetAmounts",
            value: maxTarget,
            expected: `less than or equal to ${maxBatchCapacity}`,
          },
        );
      }
    }
  }
}
