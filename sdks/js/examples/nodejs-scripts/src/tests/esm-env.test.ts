import { SparkWallet } from "@buildonspark/spark-sdk";

describe("esm environment", () => {
  it("should be able to import modules from spark-sdk", () => {
    expect(SparkWallet).toBeDefined();
  });
});
