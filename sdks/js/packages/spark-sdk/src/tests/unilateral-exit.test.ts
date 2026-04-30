import { describe, expect, it, jest } from "@jest/globals";
import type { Logger } from "@lightsparkdev/core";
import { bytesToHex } from "@noble/curves/utils";

import { TreeNode } from "../proto/spark.js";
import { Network } from "../utils/network.js";
import { constructUnilateralExitFeeBumpPackages } from "../utils/unilateral-exit.js";

describe("unilateral exit", () => {
  it("uses the provided logger for non-fatal transaction parse warnings", async () => {
    const warn = jest.fn();
    const logger = { warn } as unknown as Logger;
    const node = TreeNode.fromPartial({
      id: "node-id",
      nodeTx: new Uint8Array([1, 2, 3]),
      refundTx: new Uint8Array([4, 5, 6]),
      status: "AVAILABLE",
    });

    await expect(
      constructUnilateralExitFeeBumpPackages(
        [bytesToHex(TreeNode.encode(node).finish())],
        [],
        { satPerVbyte: 5 },
        Network.LOCAL,
        undefined,
        logger,
      ),
    ).rejects.toThrow("No UTXOs available for fee bump");

    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith(
      expect.stringContaining(
        "constructUnilateralExitFeeBumpPackages: unable to parse nodeTx",
      ),
    );
  });
});
