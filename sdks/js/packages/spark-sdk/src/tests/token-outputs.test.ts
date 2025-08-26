import { numberToBytesBE } from "@noble/curves/utils";
import { ValidationError } from "../errors/types.js";
import { OutputWithPreviousTransactionData } from "../proto/spark.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection.js";
import { TokenTransactionService } from "../services/token-transactions.js";

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

    it("should throw ValidationError when tokenAmount is 0", () => {
      const tokenOutputs = [createMockTokenOutput("output1", 100n)];

      expect(() =>
        tokenTransactionService.selectTokenOutputs(
          tokenOutputs,
          0n,
          "SMALL_FIRST",
        ),
      ).toThrow(ValidationError);
    });

    it("should throw ValidationError when available token amount is less than needed", () => {
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
      ).toThrow(ValidationError);
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
});
