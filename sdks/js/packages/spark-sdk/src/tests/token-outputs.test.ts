import { numberToBytesBE, bytesToNumberBE } from "@noble/curves/utils";
import { SparkValidationError } from "../errors/types.js";
import {
  OutputWithPreviousTransactionData,
  TokenOutputStatus,
} from "../proto/spark_token.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection/connection.js";
import {
  MAX_TOKEN_OUTPUTS_TX,
  TokenTransactionService,
} from "../services/tokens/token-transactions.js";
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
    tokenIdentifier: Uint8Array = new Uint8Array(32).fill(5),
  ): OutputWithPreviousTransactionData => ({
    output: {
      id,
      ownerPublicKey,
      tokenPublicKey,
      tokenIdentifier,
      tokenAmount: numberToBytesBE(tokenAmount, 16),
      revocationCommitment: new Uint8Array(32).fill(3),
      status: TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
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

    it("should throw SparkValidationError when tokenOutputs map is missing a receiver token key", async () => {
      const tokenIdentifier =
        "btknrt1qqtpc5vxamq522ch9r89kkt0sgpfd2zac7uzvz4akhsmy2jrsjnsk6xuae" as Bech32mTokenIdentifier;

      await expect(
        tokenTransactionService.tokenTransfer({
          tokenOutputs: new Map(),
          receiverOutputs: [
            {
              tokenIdentifier,
              tokenAmount: 100n,
              receiverSparkAddress: "spark1invalid",
            },
          ],
          outputSelectionStrategy: "SMALL_FIRST",
        }),
      ).rejects.toThrow(SparkValidationError);
    });
  });

  describe("per-token output limit validation", () => {
    const validateOutputCountPerToken = (
      outputsToUse: OutputWithPreviousTransactionData[],
    ) =>
      (tokenTransactionService as any)["validateOutputCountPerToken"](
        outputsToUse,
      );

    it("allows multi-token transfers when each token is within the limit", () => {
      const tokenA = new Uint8Array(32).fill(10);
      const tokenB = new Uint8Array(32).fill(11);
      const ownerA = new Uint8Array(32).fill(20);
      const ownerB = new Uint8Array(32).fill(21);

      const tokenAOutputs = Array.from(
        { length: MAX_TOKEN_OUTPUTS_TX },
        (_, i) => createMockTokenOutput(`a-${i}`, 1n, tokenA, ownerA, tokenA),
      );
      const tokenBOutputs = Array.from(
        { length: MAX_TOKEN_OUTPUTS_TX },
        (_, i) => createMockTokenOutput(`b-${i}`, 1n, tokenB, ownerB, tokenB),
      );

      expect(() =>
        validateOutputCountPerToken([...tokenAOutputs, ...tokenBOutputs]),
      ).not.toThrow();
    });

    it("rejects transfers that exceed the limit for a single token type", () => {
      const tokenA = new Uint8Array(32).fill(10);
      const ownerA = new Uint8Array(32).fill(20);

      const tokenAOutputs = Array.from(
        { length: MAX_TOKEN_OUTPUTS_TX + 1 },
        (_, i) => createMockTokenOutput(`a-${i}`, 1n, tokenA, ownerA, tokenA),
      );

      expect(() => validateOutputCountPerToken(tokenAOutputs)).toThrow(
        SparkValidationError,
      );
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
      status: TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
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

      const retrieved = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(retrieved).toHaveLength(2);
      expect(retrieved[0]!.output!.id).toBe("out1");
      expect(retrieved[1]!.output!.id).toBe("out2");
    });

    it("should return empty array for unknown token", async () => {
      const manager = new TokenOutputManager();
      const retrieved = await manager.getAvailableOutputs(TOKEN_ID_1);
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
      await manager.lockOutputsByIds(["out1"]);

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
      await manager.lockOutputs([output]);

      expect(await manager.isLocked("out1")).toBe(true);
    });

    it("should lock outputs by ID", async () => {
      const manager = new TokenOutputManager();
      const map: TokenOutputsMap = new Map([
        [TOKEN_ID_1, [createMockOutput("out1")]],
      ]);

      await manager.setOutputs(map);
      await manager.lockOutputsByIds(["out1"], "op-123");

      expect(await manager.isLocked("out1")).toBe(true);
    });

    it("should release outputs by data", async () => {
      const manager = new TokenOutputManager();
      const output = createMockOutput("out1");
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, [output]]]);

      await manager.setOutputs(map);
      await manager.lockOutputs([output]);
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
      await manager.lockOutputsByIds(["out1"]);
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
      await manager.lockOutputsByIds(["out1"]);

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
      await manager.lockOutputsByIds(["out1"]);

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
      await manager.lockOutputsByIds(["out1"]);

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

  describe("setOutputs syncing behavior", () => {
    const createMockOutputWithStatus = (
      id: string,
      status: TokenOutputStatus,
      tokenAmount: bigint = 100n,
    ): OutputWithPreviousTransactionData => ({
      output: {
        id,
        ownerPublicKey: new Uint8Array(32).fill(1),
        tokenPublicKey: new Uint8Array(32).fill(2),
        tokenAmount: numberToBytesBE(tokenAmount, 16),
        revocationCommitment: new Uint8Array(32).fill(3),
        status,
      },
      previousTransactionHash: new Uint8Array(32).fill(4),
      previousTransactionVout: 0,
    });

    it("should separate available and pending outbound outputs", async () => {
      const manager = new TokenOutputManager();
      const outputs = [
        createMockOutputWithStatus(
          "available1",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
        createMockOutputWithStatus(
          "pending1",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
        ),
        createMockOutputWithStatus(
          "available2",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
      ];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);

      const available = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(available).toHaveLength(2);
      expect(available.map((o) => o.output!.id)).toEqual([
        "available1",
        "available2",
      ]);

      const pending = await manager.getPendingOutboundOutputs(TOKEN_ID_1);
      expect(pending).toHaveLength(1);
      expect(pending[0]!.output!.id).toBe("pending1");
    });

    it("should remove local lock when output becomes pending on server", async () => {
      const manager = new TokenOutputManager();

      // Start with available outputs
      const initialOutputs = [
        createMockOutputWithStatus(
          "out1",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
        createMockOutputWithStatus(
          "out2",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
      ];
      await manager.setOutputs(new Map([[TOKEN_ID_1, initialOutputs]]));

      // Lock an output locally (simulating a transaction in progress)
      await manager.lockOutputsByIds(["out1"]);
      expect(await manager.isLocked("out1")).toBe(true);

      // Sync with server - out1 is now pending on server
      const updatedOutputs = [
        createMockOutputWithStatus(
          "out1",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
        ),
        createMockOutputWithStatus(
          "out2",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
      ];
      await manager.setOutputs(new Map([[TOKEN_ID_1, updatedOutputs]]));

      // Local lock should be removed since server now tracks it as pending
      expect(await manager.isLocked("out1")).toBe(false);

      // out1 should be in pending, not available
      const available = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(available).toHaveLength(1);
      expect(available[0]!.output!.id).toBe("out2");

      const pending = await manager.getPendingOutboundOutputs(TOKEN_ID_1);
      expect(pending).toHaveLength(1);
      expect(pending[0]!.output!.id).toBe("out1");
    });

    it("should update only specified tokens when tokenIdentifiers provided", async () => {
      const manager = new TokenOutputManager();

      // Set up initial outputs for two tokens
      const initialMap: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "token1_out1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
        [
          TOKEN_ID_2,
          [
            createMockOutputWithStatus(
              "token2_out1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(initialMap);

      // Update only TOKEN_ID_1 with new outputs
      const updatedMap: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "token1_out2",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(updatedMap, [TOKEN_ID_1]);

      // TOKEN_ID_1 should have the new output
      const token1Outputs = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(token1Outputs).toHaveLength(1);
      expect(token1Outputs[0]!.output!.id).toBe("token1_out2");

      // TOKEN_ID_2 should still have its original output (preserved)
      const token2Outputs = await manager.getAvailableOutputs(TOKEN_ID_2);
      expect(token2Outputs).toHaveLength(1);
      expect(token2Outputs[0]!.output!.id).toBe("token2_out1");
    });

    it("should delete token from map when updated with empty outputs", async () => {
      const manager = new TokenOutputManager();

      const initialMap: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "out1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(initialMap);

      expect(await manager.hasTokenIdentifier(TOKEN_ID_1)).toBe(true);

      // Update with empty outputs for TOKEN_ID_1
      const emptyMap: TokenOutputsMap = new Map([[TOKEN_ID_1, []]]);
      await manager.setOutputs(emptyMap, [TOKEN_ID_1]);

      expect(await manager.hasTokenIdentifier(TOKEN_ID_1)).toBe(false);
    });

    it("should ignore outputs with unspecified status", async () => {
      const manager = new TokenOutputManager();
      const outputs = [
        createMockOutputWithStatus(
          "available",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
        createMockOutputWithStatus(
          "unspecified",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_UNSPECIFIED,
        ),
        createMockOutputWithStatus(
          "pending",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
        ),
      ];
      const map: TokenOutputsMap = new Map([[TOKEN_ID_1, outputs]]);

      await manager.setOutputs(map);

      const available = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(available).toHaveLength(1);
      expect(available[0]!.output!.id).toBe("available");

      const pending = await manager.getPendingOutboundOutputs(TOKEN_ID_1);
      expect(pending).toHaveLength(1);
      expect(pending[0]!.output!.id).toBe("pending");
    });

    it("should replace all outputs when no tokenIdentifiers provided", async () => {
      const manager = new TokenOutputManager();

      // Set initial outputs
      const initialMap: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "out1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
        [
          TOKEN_ID_2,
          [
            createMockOutputWithStatus(
              "out2",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(initialMap);

      // Replace all with only TOKEN_ID_1
      const newMap: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "new_out1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(newMap);

      // TOKEN_ID_1 should have the new output
      const token1Outputs = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(token1Outputs).toHaveLength(1);
      expect(token1Outputs[0]!.output!.id).toBe("new_out1");

      // TOKEN_ID_2 should be gone
      expect(await manager.hasTokenIdentifier(TOKEN_ID_2)).toBe(false);
    });

    it("should handle transition from pending to available", async () => {
      const manager = new TokenOutputManager();

      // Start with pending output
      const pendingOutputs = [
        createMockOutputWithStatus(
          "out1",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
        ),
      ];
      await manager.setOutputs(new Map([[TOKEN_ID_1, pendingOutputs]]));

      let pending = await manager.getPendingOutboundOutputs(TOKEN_ID_1);
      expect(pending).toHaveLength(1);
      let available = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(available).toHaveLength(0);

      // Transaction completes - output becomes available again
      const availableOutputs = [
        createMockOutputWithStatus(
          "out1",
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
        ),
      ];
      await manager.setOutputs(new Map([[TOKEN_ID_1, availableOutputs]]));

      pending = await manager.getPendingOutboundOutputs(TOKEN_ID_1);
      expect(pending).toHaveLength(0);
      available = await manager.getAvailableOutputs(TOKEN_ID_1);
      expect(available).toHaveLength(1);
      expect(available[0]!.output!.id).toBe("out1");
    });

    it("should handle multiple tokens with mixed statuses", async () => {
      const manager = new TokenOutputManager();

      const map: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "t1_avail",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
            ),
            createMockOutputWithStatus(
              "t1_pending",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
            ),
          ],
        ],
        [
          TOKEN_ID_2,
          [
            createMockOutputWithStatus(
              "t2_pending1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
            ),
            createMockOutputWithStatus(
              "t2_pending2",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(map);

      // TOKEN_ID_1: 1 available, 1 pending
      expect(await manager.getAvailableOutputs(TOKEN_ID_1)).toHaveLength(1);
      expect(await manager.getPendingOutboundOutputs(TOKEN_ID_1)).toHaveLength(
        1,
      );

      // TOKEN_ID_2: 0 available, 2 pending
      expect(await manager.getAvailableOutputs(TOKEN_ID_2)).toHaveLength(0);
      expect(await manager.getPendingOutboundOutputs(TOKEN_ID_2)).toHaveLength(
        2,
      );

      // Both tokens should be tracked
      const identifiers = await manager.getTokenIdentifiers();
      expect(identifiers).toHaveLength(2);
      expect(identifiers).toContain(TOKEN_ID_1);
      expect(identifiers).toContain(TOKEN_ID_2);
    });

    it("should count pending tokens in size and hasTokenIdentifier", async () => {
      const manager = new TokenOutputManager();

      // Token with only pending outputs (no available)
      const map: TokenOutputsMap = new Map([
        [
          TOKEN_ID_1,
          [
            createMockOutputWithStatus(
              "pending1",
              TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
            ),
          ],
        ],
      ]);
      await manager.setOutputs(map);

      // Even though there are no available outputs, the token should be tracked
      expect(await manager.hasTokenIdentifier(TOKEN_ID_1)).toBe(true);
      expect(await manager.size()).toBe(1);
      expect(await manager.isEmpty()).toBe(false);
    });
  });
});
