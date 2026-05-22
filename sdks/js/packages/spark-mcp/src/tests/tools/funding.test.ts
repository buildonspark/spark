import {
  describe,
  it,
  expect,
  jest,
  beforeEach,
  afterEach,
} from "@jest/globals";
import { handleFundAddress } from "../../tools/funding.js";

type MockResponse = {
  ok: boolean;
  json: () => Promise<unknown>;
};

function makeFetch(results: unknown[]): jest.MockedFunction<typeof fetch> {
  let call = 0;
  return jest.fn<typeof fetch>().mockImplementation(() => {
    const result = results[call++];
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve(result),
    } as MockResponse as Response);
  });
}

function clearEnv() {
  delete process.env["BITCOIN_NETWORK"];
  delete process.env["SPARK_LOCAL_INGRESS_HOST"];
  delete process.env["BITCOIN_RPC_URL"];
  delete process.env["BITCOIN_RPC_USER"];
  delete process.env["BITCOIN_RPC_PASSWORD"];
}

beforeEach(() => {
  clearEnv();
  process.env["BITCOIN_NETWORK"] = "LOCAL";
});

afterEach(() => {
  clearEnv();
});

describe("handleFundAddress", () => {
  it("sends funds and mines blocks, returns txid", async () => {
    const mockFetch = makeFetch([
      { result: "abc123txid", error: null }, // sendtoaddress
      { result: "bcrt1qmining", error: null }, // getnewaddress
      { result: ["blockhash1"], error: null }, // generatetoaddress
    ]);

    const result = await handleFundAddress(
      "bcrt1qtest",
      50_000,
      6,
      mockFetch,
      0,
    );

    expect(result.isError).toBeFalsy();
    expect(result.content[0]?.text).toContain("abc123txid");
    expect(result.content[0]?.text).toContain("50,000 sats");
    expect(result.content[0]?.text).toContain("6 blocks");
  });

  it("uses default amount of 50,000 sats", async () => {
    const mockFetch = makeFetch([
      { result: "txid1", error: null },
      { result: "bcrt1qmining", error: null },
      { result: ["blockhash1"], error: null },
    ]);

    await handleFundAddress("bcrt1qtest", undefined, undefined, mockFetch, 0);

    const sendCall = mockFetch.mock.calls[0];
    const body = JSON.parse(sendCall[1]!.body as string) as {
      params: [string, number];
    };
    // 50,000 sats = 0.0005 BTC
    expect(body.params[1]).toBeCloseTo(0.0005, 8);
  });

  it("mines 1 block by default", async () => {
    const mockFetch = makeFetch([
      { result: "txid1", error: null },
      { result: "bcrt1qmining", error: null },
      { result: ["blockhash1"], error: null },
    ]);

    await handleFundAddress("bcrt1qtest", undefined, undefined, mockFetch, 0);

    const generateCall = mockFetch.mock.calls[2];
    const body = JSON.parse(generateCall[1]!.body as string) as {
      params: [number, string];
    };
    expect(body.params[0]).toBe(1);
  });

  it("uses SPARK_LOCAL_INGRESS_HOST for RPC URL when set", async () => {
    process.env["SPARK_LOCAL_INGRESS_HOST"] = "192.168.49.2";
    const mockFetch = makeFetch([
      { result: "txid1", error: null },
      { result: "bcrt1qmining", error: null },
      { result: ["blockhash1"], error: null },
    ]);

    await handleFundAddress("bcrt1qtest", 10_000, 1, mockFetch, 0);

    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toBe("http://192.168.49.2:8332");
  });

  it("uses BITCOIN_RPC_URL when explicitly set", async () => {
    process.env["BITCOIN_RPC_URL"] = "http://custom-host:9332";
    const mockFetch = makeFetch([
      { result: "txid1", error: null },
      { result: "bcrt1qmining", error: null },
      { result: ["blockhash1"], error: null },
    ]);

    await handleFundAddress("bcrt1qtest", 10_000, 1, mockFetch, 0);

    const url = mockFetch.mock.calls[0][0] as string;
    expect(url).toBe("http://custom-host:9332");
  });

  it("returns error on MAINNET network", async () => {
    process.env["BITCOIN_NETWORK"] = "MAINNET";
    const mockFetch = jest.fn<typeof fetch>();

    const result = await handleFundAddress("bc1qtest", 50_000, 6, mockFetch, 0);

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("LOCAL");
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("returns error on REGTEST network", async () => {
    process.env["BITCOIN_NETWORK"] = "REGTEST";
    const mockFetch = jest.fn<typeof fetch>();

    const result = await handleFundAddress(
      "bcrt1qtest",
      50_000,
      6,
      mockFetch,
      0,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("LOCAL");
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("returns error with MAINNET override on LOCAL default", async () => {
    const mockFetch = jest.fn<typeof fetch>();

    const result = await handleFundAddress(
      "bc1qtest",
      50_000,
      6,
      mockFetch,
      0,
      "normal",
      "MAINNET",
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("LOCAL");
    expect(mockFetch).not.toHaveBeenCalled();
  });

  it("returns error when RPC call fails", async () => {
    const mockFetch = jest.fn<typeof fetch>().mockResolvedValue({
      ok: true,
      json: () =>
        Promise.resolve({
          result: null,
          error: { message: "insufficient funds" },
        }),
    } as MockResponse as Response);

    const result = await handleFundAddress(
      "bcrt1qtest",
      50_000,
      6,
      mockFetch,
      0,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("insufficient funds");
  });

  it("returns error when RPC HTTP request fails", async () => {
    const mockFetch = jest.fn<typeof fetch>().mockResolvedValue({
      ok: false,
      status: 401,
      json: () => Promise.resolve({}),
    } as unknown as Response);

    const result = await handleFundAddress(
      "bcrt1qtest",
      50_000,
      6,
      mockFetch,
      0,
    );

    expect(result.isError).toBe(true);
    expect(result.content[0]?.text).toContain("401");
  });
});
