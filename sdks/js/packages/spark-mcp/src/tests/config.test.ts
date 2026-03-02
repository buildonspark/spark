import { describe, it, expect, beforeEach } from "@jest/globals";
import { getServerConfig } from "../config.js";

function clearEnv() {
  delete process.env["BITCOIN_NETWORK"];
  delete process.env["SPARK_ENVIRONMENT"];
}

beforeEach(() => {
  clearEnv();
});

describe("getServerConfig", () => {
  it("defaults to REGTEST when BITCOIN_NETWORK is not set", () => {
    const config = getServerConfig();
    expect(config.defaultNetwork).toBe("REGTEST");
  });

  it("reads BITCOIN_NETWORK=MAINNET", () => {
    process.env["BITCOIN_NETWORK"] = "MAINNET";
    const config = getServerConfig();
    expect(config.defaultNetwork).toBe("MAINNET");
  });

  it("reads BITCOIN_NETWORK=REGTEST", () => {
    process.env["BITCOIN_NETWORK"] = "REGTEST";
    const config = getServerConfig();
    expect(config.defaultNetwork).toBe("REGTEST");
  });

  it("reads BITCOIN_NETWORK=LOCAL", () => {
    process.env["BITCOIN_NETWORK"] = "LOCAL";
    const config = getServerConfig();
    expect(config.defaultNetwork).toBe("LOCAL");
  });

  it("rejects invalid BITCOIN_NETWORK", () => {
    process.env["BITCOIN_NETWORK"] = "TESTNET";
    expect(() => getServerConfig()).toThrow("Invalid BITCOIN_NETWORK");
  });

  it("ignores SPARK_ENVIRONMENT if present (backwards compat)", () => {
    process.env["BITCOIN_NETWORK"] = "MAINNET";
    process.env["SPARK_ENVIRONMENT"] = "DEV";
    const config = getServerConfig();
    expect(config.defaultNetwork).toBe("MAINNET");
  });
});
