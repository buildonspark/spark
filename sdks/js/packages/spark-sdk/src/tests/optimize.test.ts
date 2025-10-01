import { describe, expect, it } from "@jest/globals";
import {
  greedyLeaves,
  swapMinimizingLeaves,
  maximizeUnilateralExit,
  minimizeTransferSwap,
  optimize,
  Swap,
  shouldOptimize,
} from "../utils/optimize.js";

describe("optimize", () => {
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

  it("test shouldOptimize for unilateral exit", () => {
    expect(shouldOptimize([16], 0)).toEqual(false);
    expect(shouldOptimize([16, 16], 0)).toEqual(false);
    expect(shouldOptimize([16, 16, 16, 16, 16, 16, 16, 16], 0)).toEqual(true);
  });

  it("test shouldOptimize for swap minimization", () => {
    expect(shouldOptimize([2], 1)).toEqual(false);
    expect(shouldOptimize([64], 1)).toEqual(true);
  });

  it("test optimize for unilateral exit", () => {
    expect(optimize([8], 0)).toEqual([]);
    expect(optimize([16], 0)).toEqual([]);
    expect(optimize([16, 16, 16, 16, 16, 16, 16, 16], 0)).toEqual([
      new Swap([16, 16, 16, 16, 16, 16, 16, 16], [128]),
    ]);
    expect(optimize([100000], 0)).toEqual([
      new Swap([100000], [32, 128, 512, 1024, 32768, 65536]),
    ]);
  });

  it("test optimize for swap minimization", () => {
    expect(optimize([8], 1)).toEqual([new Swap([8], [1, 1, 2, 4])]);
    expect(optimize([1, 4], 1)).toEqual([new Swap([4], [2, 2])]);
    expect(optimize([1, 16], 1)).toEqual([new Swap([16], [2, 2, 4, 8])]);
  });
});
