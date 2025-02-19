import { TreeNode } from "../proto/spark";

const MINIMUM_VALUE = 16;

// TODO: Do we even need this?
export type LeafNode = TreeNode & {
  isUsed: boolean;
};

// Selects leaves that have a sum >= targetAmount
export function selectLeaves(
  leaves: LeafNode[],
  targetAmount: number
): LeafNode[] {
  if (targetAmount < MINIMUM_VALUE) {
    throw new Error(
      `Target amount is too small to be processed: requested ${targetAmount} but minimum is ${MINIMUM_VALUE}`
    );
  }

  // Sort in ascending order
  leaves.sort((a, b) => a.value - b.value);

  // First try to find a single leaf that satisfies the target amount
  const singleLeaf = leaves.find((leaf) => leaf.value >= targetAmount);
  if (singleLeaf) {
    singleLeaf.isUsed = true;
    return [singleLeaf];
  }

  // If we don't find a single leaf that satisfies the target amount, we need to
  // select multiple leaves
  let currentSum = 0;
  const selectedLeaves: LeafNode[] = [];
  // Iterate backwards through the leaves to select the largest leaf first
  for (let i = leaves.length - 1; i >= 0; i--) {
    currentSum += leaves[i].value;
    selectedLeaves.push(leaves[i]);
    if (currentSum >= targetAmount) {
      break;
    }

    // Try to find the smallest leaf that satisfies the remaining amount
    const remainingSum = targetAmount - currentSum;
    for (let j = 0; j < i; j++) {
      if (leaves[j].value >= remainingSum) {
        currentSum += leaves[j].value;
        selectedLeaves.push(leaves[j]);
        break;
      }
    }

    if (currentSum >= targetAmount) {
      break;
    }
  }

  if (currentSum < targetAmount) {
    throw new Error(
      `Target amount is too large to be processed: requested ${targetAmount} but only ${currentSum} is available`
    );
  }

  selectedLeaves.map((leaf) => (leaf.isUsed = true));

  return selectedLeaves;
}
