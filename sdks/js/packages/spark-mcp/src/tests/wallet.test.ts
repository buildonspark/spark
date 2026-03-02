import { describe, it, expect, jest, beforeEach } from "@jest/globals";
import { resolveWallet, createFreshWallet } from "../wallet.js";

type InitializeFn = Parameters<typeof resolveWallet>[1];

function clearEnv() {
  delete process.env["SPARK_MNEMONIC"];
  delete process.env["BITCOIN_NETWORK"];
}

describe("resolveWallet", () => {
  let mockInitialize: jest.MockedFunction<NonNullable<InitializeFn>>;

  beforeEach(() => {
    mockInitialize = jest.fn<NonNullable<InitializeFn>>();
    mockInitialize.mockResolvedValue({
      wallet: { getBalance: jest.fn() } as unknown as Awaited<
        ReturnType<NonNullable<InitializeFn>>
      >["wallet"],
      mnemonic: "test mnemonic",
    });
    clearEnv();
  });

  it("uses the provided mnemonic", async () => {
    const mnemonic =
      "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";

    await resolveWallet(mnemonic, mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({ mnemonicOrSeed: mnemonic }),
    );
  });

  it("falls back to SPARK_MNEMONIC env var when no mnemonic passed", async () => {
    process.env["SPARK_MNEMONIC"] =
      "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";

    await resolveWallet(undefined, mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        mnemonicOrSeed:
          "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
      }),
    );
  });

  it("throws a clear error when neither mnemonic nor SPARK_MNEMONIC is set", async () => {
    await expect(resolveWallet(undefined, mockInitialize)).rejects.toThrow(
      "No wallet specified",
    );
  });

  it("defaults to REGTEST when BITCOIN_NETWORK is not set", async () => {
    await resolveWallet("some mnemonic", mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "REGTEST" }),
      }),
    );
  });

  it("uses MAINNET when configured", async () => {
    process.env["BITCOIN_NETWORK"] = "MAINNET";

    await resolveWallet("some mnemonic", mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "MAINNET" }),
      }),
    );
  });

  it("uses LOCAL when configured", async () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";

    await resolveWallet("some mnemonic", mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "LOCAL" }),
      }),
    );
  });

  it("allows per-call network override", async () => {
    process.env["BITCOIN_NETWORK"] = "MAINNET";

    await resolveWallet("some mnemonic", mockInitialize, "REGTEST");

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "REGTEST" }),
      }),
    );
  });

  it("allows per-call LOCAL override from non-LOCAL default", async () => {
    process.env["BITCOIN_NETWORK"] = "REGTEST";

    await resolveWallet("some mnemonic", mockInitialize, "LOCAL");

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "LOCAL" }),
      }),
    );
  });
});

describe("createFreshWallet", () => {
  beforeEach(() => {
    clearEnv();
  });

  it("defaults to REGTEST when BITCOIN_NETWORK is not set", async () => {
    const mockInitialize = jest.fn<NonNullable<InitializeFn>>();
    mockInitialize.mockResolvedValue({
      wallet: { getBalance: jest.fn() } as unknown as Awaited<
        ReturnType<NonNullable<InitializeFn>>
      >["wallet"],
      mnemonic: "generated mnemonic phrase here",
    });

    await createFreshWallet(mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "REGTEST" }),
      }),
    );
  });

  it("returns both wallet and generated mnemonic", async () => {
    const mockInitialize = jest.fn<NonNullable<InitializeFn>>();
    mockInitialize.mockResolvedValue({
      wallet: { getBalance: jest.fn() } as unknown as Awaited<
        ReturnType<NonNullable<InitializeFn>>
      >["wallet"],
      mnemonic:
        "word1 word2 word3 word4 word5 word6 word7 word8 word9 word10 word11 word12",
    });
    process.env["BITCOIN_NETWORK"] = "MAINNET";

    const { wallet, mnemonic } = await createFreshWallet(mockInitialize);

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({ mnemonicOrSeed: undefined }),
    );
    expect(wallet).toBeDefined();
    expect(mnemonic).toBe(
      "word1 word2 word3 word4 word5 word6 word7 word8 word9 word10 word11 word12",
    );
  });

  it("throws if the SDK returns no mnemonic", async () => {
    const mockInitialize = jest.fn<NonNullable<InitializeFn>>();
    mockInitialize.mockResolvedValue({
      wallet: {} as never,
      mnemonic: undefined,
    });

    await expect(createFreshWallet(mockInitialize)).rejects.toThrow(
      "no mnemonic",
    );
  });

  it("allows per-call network override", async () => {
    const mockInitialize = jest.fn<NonNullable<InitializeFn>>();
    mockInitialize.mockResolvedValue({
      wallet: { getBalance: jest.fn() } as unknown as Awaited<
        ReturnType<NonNullable<InitializeFn>>
      >["wallet"],
      mnemonic: "test mnemonic phrase here for wallet creation",
    });
    process.env["BITCOIN_NETWORK"] = "MAINNET";

    await createFreshWallet(mockInitialize, "REGTEST");

    expect(mockInitialize).toHaveBeenCalledWith(
      expect.objectContaining({
        options: expect.objectContaining({ network: "REGTEST" }),
      }),
    );
  });
});
