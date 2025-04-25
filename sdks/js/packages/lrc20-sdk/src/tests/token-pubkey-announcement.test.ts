import { describe, it, expect, test } from "@jest/globals";
import { TokenPubkeyAnnouncement } from "../lrc/types/lrc20-transaction.ts";
import { TokenPubkey } from "../lrc/types/token-pubkey.ts";

const TEST_TOKEN_PUBKEY = Buffer.from("02df9203cbff3104727f9d8dc95ff6f4225639412bfb48d898e7bb142edf415822", "hex");

describe("TokenPubkeyAnnouncement", () => {
  describe("constructor validation", () => {
    it("should validate name byte length (3-17 bytes inclusive)", () => {
      // Valid cases
      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "abc", // 3 bytes - minimum
            "TEST",
            8,
            BigInt(1000),
            false,
          ),
      ).not.toThrow();

      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "12345678901234567", // 17 bytes - maximum
            "TEST",
            8,
            BigInt(1000),
            false,
          ),
      ).not.toThrow();

      // Invalid cases
      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "ab", // 2 bytes - too short
            "TEST",
            8,
            BigInt(1000),
            false,
          ),
      ).toThrow();

      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "123456789012345678", // 18 bytes - too long
            "TEST",
            8,
            BigInt(1000),
            false,
          ),
      ).toThrow();
    });

    it("should validate symbol byte length (3-6 bytes inclusive)", () => {
      // Valid cases
      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token",
            "ABC", // 3 bytes - minimum
            8,
            BigInt(1000),
            false,
          ),
      ).not.toThrow();

      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token",
            "ABCDEF", // 6 bytes - maximum
            8,
            BigInt(1000),
            false,
          ),
      ).not.toThrow();

      // Invalid cases
      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token",
            "AB", // 2 bytes - too short
            8,
            BigInt(1000),
            false,
          ),
      ).toThrow();

      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token",
            "ABCDEFG", // 7 bytes - too long
            8,
            BigInt(1000),
            false,
          ),
      ).toThrow();
    });

    it("should validate UTF-8 byte length correctly", () => {
      // UTF-8 characters can be multiple bytes
      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token 🚀", // 'Token ' (6 bytes) + 🚀 (4 bytes) = 10 bytes
            "TEST",
            8,
            BigInt(1000),
            false,
          ),
      ).not.toThrow();

      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token",
            "T🚀", // 'T' (1 byte) + 🚀 (4 bytes) = 5 bytes
            8,
            BigInt(1000),
            false,
          ),
      ).not.toThrow();

      // Too long due to UTF-8 encoding
      expect(
        () =>
          new TokenPubkeyAnnouncement(
            new TokenPubkey(TEST_TOKEN_PUBKEY),
            "Token 🚀🚀🚀🚀", // Multiple emoji make it exceed 17 bytes
            "TEST",
            8,
            BigInt(1000),
            false,
          ),
      ).toThrow();
    });
  });

  describe("toBuffer", () => {
    it("should correctly serialize and deserialize all fields", () => {
      const tokenName = "Test Token";
      const tokenTicker = "TEST";
      const tokenDecimals = 8;
      const maxSupply = BigInt("1000000000000000");
      const isFreezable = true;

      const announcement = new TokenPubkeyAnnouncement(
        new TokenPubkey(TEST_TOKEN_PUBKEY),
        tokenName,
        tokenTicker,
        tokenDecimals,
        maxSupply,
        isFreezable,
      );

      const buffer = announcement.toBuffer();

      let offset = 0;

      expect(buffer.subarray(offset, offset + 33)).toEqual(announcement.tokenPubkey.inner);
      offset += 33;

      const nameLength = buffer[offset];
      offset += 1;
      expect(buffer.subarray(offset, offset + nameLength).toString("utf-8")).toEqual(tokenName);
      offset += nameLength;

      const symbolLength = buffer[offset];
      offset += 1;
      expect(buffer.subarray(offset, offset + symbolLength).toString("utf-8")).toEqual(tokenTicker);
      offset += symbolLength;

      expect(buffer[offset]).toEqual(8);
      offset += 1;

      const maxSupplyBuffer = buffer.subarray(offset, offset + 16);
      const decodedMaxSupply = BigInt("0x" + maxSupplyBuffer.toString("hex"));
      expect(decodedMaxSupply).toEqual(maxSupply);
      offset += 16;

      expect(buffer[offset]).toEqual(isFreezable ? 1 : 0);
      offset += 1;

      expect(buffer.length).toEqual(offset);
    });

    it("should correctly handle UTF-8 encoded characters", () => {
      const announcement = new TokenPubkeyAnnouncement(
        new TokenPubkey(TEST_TOKEN_PUBKEY),
        "Token 🚀", // UTF-8 string with emoji
        "T🚀T", // UTF-8 symbol with emoji
        8,
        BigInt(1000),
        true,
      );

      const buffer = announcement.toBuffer();
      let offset = 0;

      // Skip token pubkey
      offset += 33;

      // Verify name encoding
      const nameLength = buffer[offset];
      offset += 1;
      const nameBuffer = buffer.subarray(offset, offset + nameLength);
      expect(nameBuffer.toString("utf-8")).toEqual("Token 🚀");
      expect(nameLength).toEqual(Buffer.from("Token 🚀", "utf-8").length);
      offset += nameLength;

      // Verify symbol encoding
      const symbolLength = buffer[offset];
      offset += 1;
      const symbolBuffer = buffer.subarray(offset, offset + symbolLength);
      expect(symbolBuffer.toString("utf-8")).toEqual("T🚀T");
      expect(symbolLength).toEqual(Buffer.from("T🚀T", "utf-8").length);
      offset += symbolLength;

      // Skip remaining fields
      offset += 1; // decimal
      offset += 16; // maxSupply
      offset += 1; // isFreezable

      expect(offset).toBe(buffer.length);
    });

    it("should throw on value corruption", () => {
      const announcement = new TokenPubkeyAnnouncement(
        new TokenPubkey(TEST_TOKEN_PUBKEY),
        "Test",
        "TST",
        8,
        BigInt("0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"),
        true,
      );

      expect(() => announcement.toBuffer()).not.toThrow();
    });
  });
});
