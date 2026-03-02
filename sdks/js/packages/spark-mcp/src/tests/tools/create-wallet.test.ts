import { describe, it, expect, jest } from "@jest/globals";
import { handleCreateWallet } from "../../tools/create-wallet.js";

type CreateFreshFn = Parameters<typeof handleCreateWallet>[0];

describe("handleCreateWallet", () => {
  it("returns mnemonic and spark address", async () => {
    const mockCreateFresh = jest
      .fn<NonNullable<CreateFreshFn>>()
      .mockResolvedValue({
        wallet: {
          getSparkAddress: jest
            .fn<() => Promise<string>>()
            .mockResolvedValue("sparkl1abc123"),
        } as never,
        mnemonic:
          "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
      });

    const result = await handleCreateWallet(mockCreateFresh);

    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("abandon abandon");
    expect(result.content[0]?.text).toContain("sparkl1abc123");
    expect(result.content[0]?.text).toContain("mnemonic");
  });

  it("returns error when fresh wallet creation fails", async () => {
    const mockCreateFresh = jest
      .fn<NonNullable<CreateFreshFn>>()
      .mockRejectedValue(new Error("SDK unavailable"));

    const result = await handleCreateWallet(mockCreateFresh);

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("SDK unavailable");
  });
});
