import { numberToBytesBE, bytesToNumberBE } from "@noble/curves/utils";
import { SparkValidationError } from "../errors/types.js";
import { OutputWithPreviousTransactionData } from "../proto/spark_token.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection/connection.js";
import { TokenTransactionService } from "../services/tokens/token-transactions.js";
import { TokenOutputManager } from "../services/tokens/output-manager.js";
import { TokenOutputsMap } from "../spark-wallet/types.js";
import { Bech32mTokenIdentifier } from "../utils/token-identifier.js";

describe("select token outputs", () => {
  let tokenTransactionService: TokenTransactionService;

  beforeEach(() => {
    const mockConfig = {} as WalletConfigService;
    const mockConnectionManager = {} as ConnectionManager;
    tokenTransactionService = new TokenTransactionService(
      mockConfig,
      mockConnectionManager,
    );
  });

  // Helper to access the private sorting method
  const sortTokenOutputsByStrategy = (
    tokenOutputs: OutputWithPreviousTransactionData[],
    strategy: "SMALL_FIRST" | "LARGE_FIRST",
  ) => {
    // TypeScript bracket notation to access private method
    (tokenTransactionService as any)["sortTokenOutputsByStrategy"](
      tokenOutputs,
      strategy,
    );
  };

  const createMockTokenOutput = (
    id: string,
    tokenAmount: bigint,
    tokenPublicKey: Uint8Array = new Uint8Array(32).fill(1),
    ownerPublicKey: Uint8Array = new Uint8Array(32).fill(2),
  ): OutputWithPreviousTransactionData => ({
    output: {
      id,
      ownerPublicKey,
      tokenPublicKey,
      tokenAmount: numberToBytesBE(tokenAmount, 16),
      revocationCommitment: new Uint8Array(32).fill(3),
    },
    previousTransactionHash: new Uint8Array(32).fill(4),
    previousTransactionVout: 0,
  });

  describe("exact match scenarios", () => {
    it("should return exact match when available", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 100n),
        createMockTokenOutput("output2", 500n),
        createMockTokenOutput("output3", 1000n),
      ];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        500n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(1);
      expect(result[0]!.output!.id).toBe("output2");
    });
  });

  describe("SMALL_FIRST strategy", () => {
    it("should select smallest outputs first when no exact match", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 1000n),
        createMockTokenOutput("output2", 100n),
        createMockTokenOutput("output3", 300n),
      ];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        350n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(2);
      expect(result[0]!.output!.id).toBe("output2"); // 100n
      expect(result[1]!.output!.id).toBe("output3"); // 300n
      // Total: 400n >= 350n
    });

    it("should select minimum number of outputs needed", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 50n),
        createMockTokenOutput("output2", 100n),
        createMockTokenOutput("output3", 200n),
        createMockTokenOutput("output4", 1000n),
      ];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        300n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(3);
      expect(result[0]!.output!.id).toBe("output1"); // 50n
      expect(result[1]!.output!.id).toBe("output2"); // 100n
      expect(result[2]!.output!.id).toBe("output3"); // 200n
      // Total: 350n >= 300n
    });
  });

  describe("LARGE_FIRST strategy", () => {
    it("should select largest outputs first when no exact match", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 100n),
        createMockTokenOutput("output2", 1000n),
        createMockTokenOutput("output3", 300n),
      ];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        350n,
        "LARGE_FIRST",
      );

      expect(result).toHaveLength(1);
      expect(result[0]!.output!.id).toBe("output2"); // 1000n >= 350n
    });

    it("should select multiple outputs if largest is insufficient", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 100n),
        createMockTokenOutput("output2", 200n),
        createMockTokenOutput("output3", 150n),
      ];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        350n,
        "LARGE_FIRST",
      );

      expect(result).toHaveLength(2);
      expect(result[0]!.output!.id).toBe("output2"); // 200n
      expect(result[1]!.output!.id).toBe("output3"); // 150n
      // Total: 350n >= 350n
    });
  });

  describe("edge cases", () => {
    it("should handle single output that exactly matches", () => {
      const tokenOutputs = [createMockTokenOutput("output1", 500n)];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        500n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(1);
      expect(result[0]!.output!.id).toBe("output1");
    });

    it("should throw SparkValidationError when tokenAmount is 0", () => {
      const tokenOutputs = [createMockTokenOutput("output1", 100n)];

      expect(() =>
        tokenTransactionService.selectTokenOutputs(
          tokenOutputs,
          0n,
          "SMALL_FIRST",
        ),
      ).toThrow(SparkValidationError);
    });

    it("should throw SparkValidationError when available token amount is less than needed", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 100n),
        createMockTokenOutput("output2", 50n),
      ];

      expect(() =>
        tokenTransactionService.selectTokenOutputs(
          tokenOutputs,
          500n,
          "SMALL_FIRST",
        ),
      ).toThrow(SparkValidationError);
    });

    it("should select all outputs if needed", () => {
      const tokenOutputs = [
        createMockTokenOutput("output1", 100n),
        createMockTokenOutput("output2", 200n),
        createMockTokenOutput("output3", 300n),
      ];

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        600n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(3);
      // Total: 600n >= 600n
    });
  });

  describe("sorting with large amounts", () => {
    it("should sort correctly when all amounts are above 2^60", () => {
      const base = 2n ** 60n;
      const amounts = [
        base + 5000n,
        base + 100n,
        base + 1n,
        base + 10000n,
        base + 500n,
      ];

      const tokenOutputs = amounts.map((amount, i) =>
        createMockTokenOutput(`output${i}`, amount),
      );

      const smallFirstSorted = [...tokenOutputs];
      sortTokenOutputsByStrategy(smallFirstSorted, "SMALL_FIRST");

      const smallFirstAmounts = smallFirstSorted.map((o) =>
        bytesToNumberBE(o.output!.tokenAmount!),
      );
      expect(smallFirstAmounts).toEqual([
        base + 1n,
        base + 100n,
        base + 500n,
        base + 5000n,
        base + 10000n,
      ]);

      const largeFirstSorted = [...tokenOutputs];
      sortTokenOutputsByStrategy(largeFirstSorted, "LARGE_FIRST");

      const largeFirstAmounts = largeFirstSorted.map((o) =>
        bytesToNumberBE(o.output!.tokenAmount!),
      );
      expect(largeFirstAmounts).toEqual([
        base + 10000n,
        base + 5000n,
        base + 500n,
        base + 100n,
        base + 1n,
      ]);
    });
  });

  describe("500 output limit and swapping mechanism", () => {
    it("should select smallest outputs when they fit within 500 limit", () => {
      const tokenOutputs: OutputWithPreviousTransactionData[] = [];

      for (let i = 0; i < 550; i++) {
        tokenOutputs.push(createMockTokenOutput(`small${i}`, 1n));
      }

      for (let i = 0; i < 50; i++) {
        tokenOutputs.push(createMockTokenOutput(`large${i}`, 1000n));
      }

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        400n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(400);
      result.forEach((output) => {
        expect(output.output!.id).toMatch(/^small\d+$/);
      });

      const total = result.reduce(
        (sum, output) => sum + bytesToNumberBE(output.output!.tokenAmount!),
        0n,
      );
      expect(total).toBe(400n);
    });

    it("should swap small for large outputs when 500 small outputs are insufficient", () => {
      const tokenOutputs: OutputWithPreviousTransactionData[] = [];

      for (let i = 0; i < 500; i++) {
        tokenOutputs.push(createMockTokenOutput(`small${i}`, 1n));
      }

      for (let i = 0; i < 20; i++) {
        tokenOutputs.push(createMockTokenOutput(`large${i}`, 1000n));
      }

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        1200n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(500);

      const largeCount = result.filter((output) =>
        output.output!.id!.startsWith("large"),
      ).length;

      expect(largeCount).toBeGreaterThan(0);

      const total = result.reduce(
        (sum, output) => sum + bytesToNumberBE(output.output!.tokenAmount!),
        0n,
      );
      expect(total).toBeGreaterThanOrEqual(1200n);
    });

    it("should minimize large outputs used during swapping", () => {
      const tokenOutputs: OutputWithPreviousTransactionData[] = [];

      for (let i = 0; i < 500; i++) {
        tokenOutputs.push(createMockTokenOutput(`small${i}`, 10n));
      }

      for (let i = 0; i < 50; i++) {
        tokenOutputs.push(createMockTokenOutput(`large${i}`, 1000n));
      }

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        5500n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(500);

      const smallCount = result.filter((output) =>
        output.output!.id!.startsWith("small"),
      ).length;
      const largeCount = result.filter((output) =>
        output.output!.id!.startsWith("large"),
      ).length;

      expect(largeCount).toBe(1);
      expect(smallCount).toBe(499);

      const total = result.reduce(
        (sum, output) => sum + bytesToNumberBE(output.output!.tokenAmount!),
        0n,
      );
      expect(total).toBe(5990n);
    });

    it("should handle significant swapping when target is much larger than small outputs", () => {
      const tokenOutputs: OutputWithPreviousTransactionData[] = [];

      for (let i = 0; i < 500; i++) {
        tokenOutputs.push(createMockTokenOutput(`small${i}`, 1n));
      }

      for (let i = 0; i < 100; i++) {
        tokenOutputs.push(createMockTokenOutput(`large${i}`, 100n));
      }

      const result = tokenTransactionService.selectTokenOutputs(
        tokenOutputs,
        5000n,
        "SMALL_FIRST",
      );

      expect(result).toHaveLength(500);

      const largeCount = result.filter((output) =>
        output.output!.id!.startsWith("large"),
      ).length;

      expect(largeCount).toBeGreaterThanOrEqual(45);

      const total = result.reduce(
        (sum, output) => sum + bytesToNumberBE(output.output!.tokenAmount!),
        0n,
      );
      expect(total).toBeGreaterThanOrEqual(5000n);
    });
  });
});

describe("TokenOutputManager", () => {
  const TOKEN_ID_1 =
    "btknrt1qqtpc5vxamq522ch9r89kkt0sgpfd2zac7uzvz4akhsmy2jrsjnsk6xuae" as Bech32mTokenIdentifier;
  const TOKEN_ID_2 =
    "btknrt1qqtpc5vxamq522ch9r99kkt0sgpfd2zac7uzvz4akhsmy2jrsjnsk6xuae" as Bech32mTokenIdentifier;

  const createMockOutput = (
    id: string,
    tokenAmount: bigint = 100n,
  ): OutputWithPreviousTransactionData => ({
    output: {
      id,
      ownerPublicKey: new Uint8Array(32).fill(1),
      tokenPublicKey: new Uint8Array(32).fill(2),
      tokenAmount: numberToBytesBE(tokenAmount, 16),
      revocationCommitment: new Uint8Array(32).fill(3),
    },
    previousTransactionHash: new Uint8Array(32).fill(4),
    previousTransactionVout: 0,
  });

  describe("basic output operations", () => {
    it("should start empty", async () => {
      const manager = new TokenOutputManager();
      expect(await manager.isEmpty()).toBe(true);
      expect(await manager.size()).toBe(0);
    });

    it("should set and get outputs", async () => {
      const manager = new TokenOutputManager();
      const outputs = [createMockOutput("out1"), createMockOutput("out2")];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);

      expect(await manager.isEmpty()).toBe(false);
      expect(await manager.size()).toBe(1);
      expect(await manager.hasTokenIdentifier(TOKEN_ID_1)).toBe(true);
      expect(await manager.hasTokenIdentifier(TOKEN_ID_2)).toBe(false);
    });

    it("should get all outputs for a token", async () => {
      const manager = new TokenOutputManager();
      const outputs = [createMockOutput("out1"), createMockOutput("out2")];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);

      const retrieved = await manager.getAllOutputs(TOKEN_ID_1);
      expect(retrieved).toHaveLength(2);
      expect(retrieved[0]!.output!.id).toBe("out1");
      expect(retrieved[1]!.output!.id).toBe("out2");
    });

    it("should return empty array for unknown token", async () => {
      const manager = new TokenOutputManager();
      const retrieved = await manager.getAllOutputs(TOKEN_ID_1);
      expect(retrieved).toHaveLength(0);
    });

    it("should get all token identifiers", async () => {
      const manager = new TokenOutputManager();
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
        [TOKEN_ID_2, [createMockOutput("out2")]],
      ]);

      await manager.setOutputs(map);

      const identifiers = await manager.getTokenIdentifiers();
      expect(identifiers).toHaveLength(2);
      expect(identifiers).toContain(TOKEN_ID_1);
      expect(identifiers).toContain(TOKEN_ID_2);
    });

    it("should get entries snapshot", async () => {
      const manager = new TokenOutputManager();
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
        [TOKEN_ID_2, [createMockOutput("out2")]],
      ]);

      await manager.setOutputs(map);

      const entries = await manager.entries();
      expect(entries).toHaveLength(2);
    });

    it("should clear all outputs and locks", async () => {
      const manager = new TokenOutputManager();
      const outputs = [createMockOutput("out1")];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "pending_sent");

      await manager.clear();

      expect(await manager.isEmpty()).toBe(true);
      expect(await manager.isLocked("out1")).toBe(false);
    });
  });

  describe("locking mechanism", () => {
    it("should lock outputs by data", async () => {
      const manager = new TokenOutputManager();
      const output = createMockOutput("out1");
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, [output]]]);

      await manager.setOutputs(map);
      await manager.lockOutputs([output], "pending_sent");

      expect(await manager.isLocked("out1")).toBe(true);
    });

    it("should lock outputs by ID", async () => {
      const manager = new TokenOutputManager();
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
      ]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "pending_received", "op-123");

      expect(await manager.isLocked("out1")).toBe(true);
    });

    it("should release outputs by data", async () => {
      const manager = new TokenOutputManager();
      const output = createMockOutput("out1");
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, [output]]]);

      await manager.setOutputs(map);
      await manager.lockOutputs([output], "pending_sent");
      expect(await manager.isLocked("out1")).toBe(true);

      await manager.releaseOutputs([output]);
      expect(await manager.isLocked("out1")).toBe(false);
    });

    it("should release outputs by ID", async () => {
      const manager = new TokenOutputManager();
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
      ]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "pending_sent");
      expect(await manager.isLocked("out1")).toBe(true);

      await manager.releaseOutputsByIds(["out1"]);
      expect(await manager.isLocked("out1")).toBe(false);
    });
  });

  describe("acquireOutputs", () => {
    it("should acquire and lock selected outputs", async () => {
      const manager = new TokenOutputManager();
      const outputs = [
        createMockOutput("out1", 100n),
        createMockOutput("out2", 200n),
        createMockOutput("out3", 300n),
      ];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);

      const acquired = await manager.acquireOutputs(
        TOKEN_ID_1,
        (available) => available.slice(0, 2),
        "test-op",
      );

      expect(acquired.outputs).toHaveLength(2);
      expect(await manager.isLocked("out1")).toBe(true);
      expect(await manager.isLocked("out2")).toBe(true);
      expect(await manager.isLocked("out3")).toBe(false);
    });

    it("should release acquired outputs via release function", async () => {
      const manager = new TokenOutputManager();
      const outputs = [createMockOutput("out1"), createMockOutput("out2")];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);

      const acquired = await manager.acquireOutputs(TOKEN_ID_1, (available) =>
        available.slice(0, 1),
      );

      expect(await manager.isLocked("out1")).toBe(true);

      await acquired.release();

      expect(await manager.isLocked("out1")).toBe(false);
    });

    it("should return empty result when selector returns empty array", async () => {
      const manager = new TokenOutputManager();
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
      ]);

      await manager.setOutputs(map);

      const acquired = await manager.acquireOutputs(TOKEN_ID_1, () => []);

      expect(acquired.outputs).toHaveLength(0);
      expect(await manager.isLocked("out1")).toBe(false);
    });

    it("should exclude locked outputs from available outputs", async () => {
      const manager = new TokenOutputManager();
      const outputs = [createMockOutput("out1"), createMockOutput("out2")];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "pending_sent");

      const acquired = await manager.acquireOutputs(
        TOKEN_ID_1,
        (available) => available,
      );

      expect(acquired.outputs).toHaveLength(1);
      expect(acquired.outputs[0]!.output!.id).toBe("out2");
    });
  });

  describe("lock expiry", () => {
    it("should expire locks after configured time", async () => {
      const manager = new TokenOutputManager(50); // 50ms expiry
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
      ]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "pending_sent");

      expect(await manager.isLocked("out1")).toBe(true);

      await new Promise((resolve) => setTimeout(resolve, 60));

      expect(await manager.isLocked("out1")).toBe(false);
    });

    it("should make expired outputs available for acquire", async () => {
      const manager = new TokenOutputManager(50);
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
      ]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "pending_sent");

      const acquired1 = await manager.acquireOutputs(
        TOKEN_ID_1,
        (available) => available,
      );
      expect(acquired1.outputs).toHaveLength(0);

      await new Promise((resolve) => setTimeout(resolve, 60));

      const acquired2 = await manager.acquireOutputs(
        TOKEN_ID_1,
        (available) => available,
      );
      expect(acquired2.outputs).toHaveLength(1);
    });
  });

  describe("stale lock cleanup", () => {
    it("should cleanup locks for outputs that no longer exist on setOutputs", async () => {
      const manager = new TokenOutputManager();
      const outputs = [createMockOutput("out1"), createMockOutput("out2")];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1", "out2"], "pending_sent");

      const newMap: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out2")]],
      ]);
      await manager.setOutputs(newMap);

      expect(await manager.isLocked("out1")).toBe(false);
      expect(await manager.isLocked("out2")).toBe(true);
    });
  });
});
