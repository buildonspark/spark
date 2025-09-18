import { describe, expect, it } from "@jest/globals";
import {
  greedyLeaves,
  swapMinimizingLeaves,
  maximizeUnilateralExit,
  minimizeTransferSwap,
  Swap,
} from "../utils/optimize.js";

describe("keys", () => {
  it("test greedyLeaves", () => {
    expect(greedyLeaves(0)).toEqual([]);
    expect(greedyLeaves(1)).toEqual([1]);
    expect(greedyLeaves(100)).toEqual([4, 32, 64]);
    expect(greedyLeaves(255)).toEqual([1, 2, 4, 8, 16, 32, 64, 128]);
    expect(greedyLeaves(256)).toEqual([256]);
  });

  it("test swapMinimizingLeaves", () => {
    expect(swapMinimizingLeaves(0)).toEqual([]);
    expect(swapMinimizingLeaves(1)).toEqual([1]);
    expect(swapMinimizingLeaves(100)).toEqual([1, 1, 2, 4, 4, 8, 16, 32, 32]);
    expect(swapMinimizingLeaves(255)).toEqual([1, 2, 4, 8, 16, 32, 64, 128]);
    expect(swapMinimizingLeaves(256)).toEqual([1, 1, 2, 4, 8, 16, 32, 64, 128]);
  });

  it("test maximizeUnilateralExit", () => {
    expect(maximizeUnilateralExit([100, 64, 28, 1, 1])).toEqual([
      new Swap([1, 1, 28, 64, 100], [2, 64, 128]),
    ]);
    expect(maximizeUnilateralExit([1, 1, 1, 1, 1, 1, 1, 1], 2)).toEqual([
      new Swap([1, 1], [2]),
      new Swap([1, 1], [2]),
      new Swap([1, 1], [2]),
      new Swap([1, 1], [2]),
    ]);
  });

  it("test minimizeTransferSwap", () => {
    expect(minimizeTransferSwap([8])).toEqual([new Swap([8], [1, 1, 2, 4])]);
    expect(minimizeTransferSwap([100])).toEqual([
      new Swap([100], [1, 1, 2, 4, 4, 8, 16, 32, 32]),
    ]);
  });
});
