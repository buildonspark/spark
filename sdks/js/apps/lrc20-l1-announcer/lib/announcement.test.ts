import { describe, it } from "node:test";
import assert from "node:assert/strict";
import { TokenPubkeyAnnouncement } from "./announcement.js";
import { TokenPubkey } from "./types.js";

const defaultPubkey = new TokenPubkey();
const validSymbol = "TST";

function makeAnnouncement(overrides: {
  name?: string;
  symbol?: string;
  decimal?: number;
  maxSupply?: bigint;
  isFreezable?: boolean;
}) {
  return new TokenPubkeyAnnouncement(
    defaultPubkey,
    overrides.name ?? "ValidToken",
    overrides.symbol ?? validSymbol,
    overrides.decimal ?? 8,
    overrides.maxSupply ?? 1000n,
    overrides.isFreezable ?? false,
  );
}

describe("TokenPubkeyAnnouncement", () => {
  describe("name validation", () => {
    it("accepts names of 18-20 bytes", () => {
      assert.doesNotThrow(() => makeAnnouncement({ name: "a".repeat(18) }));
      assert.doesNotThrow(() => makeAnnouncement({ name: "a".repeat(19) }));
      assert.doesNotThrow(() => makeAnnouncement({ name: "a".repeat(20) }));
    });

    it("rejects names over 20 bytes", () => {
      assert.throws(
        () => makeAnnouncement({ name: "a".repeat(21) }),
        /out of range/,
      );
    });

    it("rejects names under 3 bytes", () => {
      assert.throws(() => makeAnnouncement({ name: "ab" }), /out of range/);
    });
  });

  describe("decimal validation", () => {
    it("accepts 0 and 255", () => {
      assert.doesNotThrow(() => makeAnnouncement({ decimal: 0 }));
      assert.doesNotThrow(() => makeAnnouncement({ decimal: 255 }));
    });

    it("rejects negative values", () => {
      assert.throws(() => makeAnnouncement({ decimal: -1 }), /decimal must be/);
    });

    it("rejects values over 255", () => {
      assert.throws(
        () => makeAnnouncement({ decimal: 256 }),
        /decimal must be/,
      );
    });

    it("rejects non-integer values", () => {
      assert.throws(
        () => makeAnnouncement({ decimal: 1.5 }),
        /decimal must be/,
      );
    });
  });

  describe("maxSupply validation", () => {
    it("accepts 0 and 2^128-1", () => {
      assert.doesNotThrow(() => makeAnnouncement({ maxSupply: 0n }));
      assert.doesNotThrow(() =>
        makeAnnouncement({ maxSupply: 2n ** 128n - 1n }),
      );
    });

    it("rejects negative values", () => {
      assert.throws(
        () => makeAnnouncement({ maxSupply: -1n }),
        /maxSupply must be/,
      );
    });

    it("rejects values over 2^128-1", () => {
      assert.throws(
        () => makeAnnouncement({ maxSupply: 2n ** 128n }),
        /maxSupply must be/,
      );
    });
  });

  describe("toBuffer", () => {
    it("produces a buffer for valid inputs", () => {
      const a = makeAnnouncement({});
      const buf = a.toBuffer();
      assert.ok(buf.length > 0);
    });
  });
});
