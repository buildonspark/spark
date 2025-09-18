import { InternalValidationError } from "../errors/types.js";

const DENOMINATIONS: number[] = Array.from({ length: 28 }, (_, i) => 2 ** i);

function assert(condition: boolean, message?: string): asserts condition {
  if (!condition) {
    throw new InternalValidationError(message || "Assertion failed");
  }
}

function sum(arr: number[]): number {
  return arr.reduce((a, b) => a + b, 0);
}

function sorted(arr: number[]): number[] {
  return [...arr].sort((a, b) => a - b);
}

function equals(a: number[], b: number[]): boolean {
  return a.length === b.length && a.every((val, index) => val === b[index]);
}

function countOccurrences(arr: number[]): Map<number, number> {
  const map = new Map<number, number>();
  for (const x of arr) {
    map.set(x, (map.get(x) ?? 0) + 1);
  }
  return map;
}

function subtractCounters(
  a: Map<number, number>,
  b: Map<number, number>,
): Map<number, number> {
  const result = new Map<number, number>();
  for (const [key, value] of a.entries()) {
    const diff = value - (b.get(key) ?? 0);
    if (diff > 0) {
      result.set(key, diff);
    }
  }
  return result;
}

function counterToFlatArray(counter: Map<number, number>): number[] {
  const arr: number[] = [];
  for (const [k, v] of Array.from(counter.entries()).sort(
    (a, b) => a[0] - b[0],
  )) {
    for (let i = 0; i < v; i++) {
      arr.push(k);
    }
  }
  return arr;
}

export function greedyLeaves(amount: number): number[] {
  const leaves: number[] = [];
  let remaining = amount;
  for (let i = DENOMINATIONS.length - 1; i >= 0; i--) {
    const leaf = DENOMINATIONS[i];
    if (typeof leaf === "number" && leaf > 0) {
      while (remaining >= leaf) {
        remaining -= leaf;
        leaves.push(leaf);
      }
    }
  }
  assert(sum(leaves) === amount, "greedy_leaves: sum mismatch");
  return sorted(leaves);
}

export function swapMinimizingLeaves(
  amount: number,
  multiplicity: number = 1,
): number[] {
  const leaves: number[] = [];
  let remaining = amount;
  assert(multiplicity > 0, "multiplicity must be > 0");
  for (const leaf of DENOMINATIONS) {
    if (typeof leaf === "number" && leaf > 0) {
      for (let i = 0; i < multiplicity; i++) {
        if (remaining >= leaf) {
          remaining -= leaf;
          leaves.push(leaf);
        }
      }
    }
  }
  leaves.push(...greedyLeaves(remaining));
  assert(sum(leaves) === amount, "swap_minimizing_leaves: sum mismatch");
  return sorted(leaves);
}

export class Swap {
  inLeaves: number[];
  outLeaves: number[];

  constructor(inLeaves: number[], outLeaves: number[]) {
    this.inLeaves = [...inLeaves];
    this.outLeaves = [...outLeaves];
    assert(
      sum(this.inLeaves) === sum(this.outLeaves),
      "Swap in/out leaves must sum to same value for swap: " + this.toString(),
    );
  }

  toString(): string {
    return `Swap(in=${JSON.stringify(this.inLeaves)}, out=${JSON.stringify(this.outLeaves)})`;
  }
}

/**
 * Generates swaps that will result in the unilateral exit maximizing set of leaves. Multiple iterations
 * may be required to reach the optimal set.
 *
 * @param inputLeaves - the leaves to optimize.
 * @param maxLeavesPerSwap - soft limit on the number of leaves per swap.
 * @returns - the swaps that will result in the unilateral exit maximizing set of leaves.
 */
export function maximizeUnilateralExit(
  inputLeaves: number[],
  maxLeavesPerSwap: number = 64,
): Swap[] {
  const swaps: Swap[] = [];

  let batch: number[] = [];
  let leaves: number[] = sorted(inputLeaves);

  // Process leaves in batches of up to approximately maxLeavesPerSwap.
  while (leaves.length > 0) {
    batch.push(leaves.shift()!);
    const target = greedyLeaves(sum(batch));
    if (batch.length >= maxLeavesPerSwap || target.length >= maxLeavesPerSwap) {
      if (!equals(target, batch)) {
        swaps.push(new Swap([...batch], target));
      }
      batch = [];
    }
  }

  // Process any remaining leaves.
  if (batch.length > 0) {
    const target = greedyLeaves(sum(batch));
    if (!equals(target, batch)) {
      swaps.push(new Swap([...batch], target));
    }
  }

  return swaps;
}

/**
 * Generates swaps that will minimize the probability of needing to swap during a transfer. Multiple iterations
 * may be required to reach the optimal set.
 *
 * @param inputLeaves - the leaves to optimize.
 * @param multiplicity - roughly speaking, the higher the multiplicity, the more transfers can be sent
 * without needing to swap, but setting it too high will slow things down (recommended: 1 or 2)
 * @param maxLeavesPerSwap - soft limit on the number of leaves per swap.
 * @returns - the swaps that will minimize the probability of needing to swap during a transfer.
 */
export function minimizeTransferSwap(
  inputLeaves: number[],
  multiplicity: number = 1,
  maxLeavesPerSwap: number = 64,
): Swap[] {
  const balance = sum(inputLeaves);
  const optimalLeaves = swapMinimizingLeaves(balance, multiplicity);
  const walletCounter = countOccurrences(inputLeaves);
  const optimalCounter = countOccurrences(optimalLeaves);

  const leavesToGive = subtractCounters(walletCounter, optimalCounter);
  const leavesToReceive = subtractCounters(optimalCounter, walletCounter);

  const leavesToGiveFlat = counterToFlatArray(leavesToGive);
  const leavesToReceiveFlat = counterToFlatArray(leavesToReceive);

  const swaps: Swap[] = [];
  let toGiveBatch: number[] = [];
  let toReceiveBatch: number[] = [];
  let give = [...leavesToGiveFlat];
  let receive = [...leavesToReceiveFlat];

  while (give.length > 0 || receive.length > 0) {
    if (sum(toGiveBatch) > sum(toReceiveBatch)) {
      if (receive.length === 0) break;
      toReceiveBatch.push(receive.shift()!);
    } else {
      if (give.length === 0) break;
      toGiveBatch.push(give.shift()!);
    }

    if (
      toGiveBatch.length > 0 &&
      toReceiveBatch.length > 0 &&
      sum(toGiveBatch) === sum(toReceiveBatch)
    ) {
      if (toGiveBatch.length > maxLeavesPerSwap) {
        for (let i = 0; i < toGiveBatch.length; i += maxLeavesPerSwap) {
          const subset = toGiveBatch.slice(i, i + maxLeavesPerSwap);
          swaps.push(new Swap(subset, greedyLeaves(sum(subset))));
        }
      } else if (toReceiveBatch.length > maxLeavesPerSwap) {
        for (let cutoff = maxLeavesPerSwap; cutoff > 0; cutoff--) {
          const sumCut = sum(toReceiveBatch.slice(0, cutoff));
          const remainder = sum(toGiveBatch) - sumCut;
          const alternateBatch = [
            ...toReceiveBatch.slice(0, cutoff),
            ...greedyLeaves(remainder),
          ];
          if (alternateBatch.length <= maxLeavesPerSwap) {
            swaps.push(new Swap([...toGiveBatch], alternateBatch));
            break;
          }
        }
      } else {
        swaps.push(new Swap([...toGiveBatch], [...toReceiveBatch]));
      }
      toGiveBatch = [];
      toReceiveBatch = [];
    }
  }

  return swaps;
}
