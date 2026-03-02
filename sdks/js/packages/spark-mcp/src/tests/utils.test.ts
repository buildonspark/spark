import { describe, it, expect } from "@jest/globals";
import { formatSats, errorMessage, formatTransferList } from "../utils.js";

describe("formatSats", () => {
  it("formats small amounts", () => {
    expect(formatSats(1n)).toBe("1 sats");
    expect(formatSats(100n)).toBe("100 sats");
  });

  it("formats thousands with commas", () => {
    expect(formatSats(1250n)).toBe("1,250 sats");
    expect(formatSats(1000000n)).toBe("1,000,000 sats");
  });

  it("formats zero", () => {
    expect(formatSats(0n)).toBe("0 sats");
  });
});

describe("errorMessage", () => {
  it("extracts message from Error", () => {
    expect(errorMessage(new Error("connection failed"))).toBe(
      "connection failed",
    );
  });

  it("stringifies non-Error values", () => {
    expect(errorMessage("raw string")).toBe("raw string");
    expect(errorMessage(42)).toBe("42");
  });

  it("handles null/undefined", () => {
    expect(errorMessage(null)).toBe("Unknown error");
    expect(errorMessage(undefined)).toBe("Unknown error");
  });
});

describe("formatTransferList", () => {
  it("returns empty message for empty list", () => {
    expect(formatTransferList([])).toBe("No transfers found.");
  });

  it("limits to 10 items by default", () => {
    const items = Array.from({ length: 15 }, (_, i) => `item${i}`);
    const result = formatTransferList(items);
    const lines = result.split("\n");
    expect(lines).toHaveLength(11); // header + 10 items
    expect(result).toContain("(showing 10 of 15)");
  });

  it("formats all items when fewer than limit", () => {
    const items = ["a", "b", "c"];
    const result = formatTransferList(items);
    expect(result).toContain("a");
    expect(result).toContain("b");
    expect(result).toContain("c");
    expect(result).not.toContain("showing");
  });
});
