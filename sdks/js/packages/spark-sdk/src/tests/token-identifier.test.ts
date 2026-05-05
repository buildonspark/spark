import { describe, expect, test } from "@jest/globals";
import { Network, type NetworkType } from "../utils/network.js";
import {
  decodeBech32mTokenIdentifier,
  encodeBech32mTokenIdentifier,
} from "../utils/token-identifier.js";

const TEST_TOKEN_IDENTIFIER = new Uint8Array([
  63, 122, 103, 46, 122, 5, 97, 185, 253, 135, 91, 94, 115, 80, 198, 19, 246,
  106, 151, 26, 124, 57, 156, 44, 26, 105, 66, 164, 126, 75, 150, 248,
]);

const getExpectedTokenIdentifier = (network: NetworkType) => {
  switch (network) {
    case "MAINNET":
      return "btkn18aaxwtn6q4smnlv8td08x5xxz0mx49c60suectq6d9p2gljtjmuqahuvew";
    case "TESTNET":
      return "btknt18aaxwtn6q4smnlv8td08x5xxz0mx49c60suectq6d9p2gljtjmuqlps2vm";
    case "SIGNET":
      return "btkns18aaxwtn6q4smnlv8td08x5xxz0mx49c60suectq6d9p2gljtjmuqsem3dn";
    case "REGTEST":
      return "btknrt18aaxwtn6q4smnlv8td08x5xxz0mx49c60suectq6d9p2gljtjmuq7f86q9";
    case "LOCAL":
      return "btknl18aaxwtn6q4smnlv8td08x5xxz0mx49c60suectq6d9p2gljtjmuqdl8f83";
  }
};

describe("token identifier", () => {
  test("encodeBech32mTokenIdentifier", () => {
    const netKeys = Object.values(Network).filter((v) => isNaN(Number(v)));
    for (const network of netKeys) {
      const tokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: TEST_TOKEN_IDENTIFIER,
        network: network as NetworkType,
      });
      expect(tokenIdentifier).toBe(
        getExpectedTokenIdentifier(network as NetworkType),
      );
    }
  });

  test("decodeBech32mTokenIdentifier", () => {
    const netKeys = Object.values(Network).filter((v) => isNaN(Number(v)));
    for (const network of netKeys) {
      const identifier = getExpectedTokenIdentifier(network as NetworkType);
      const decoded = decodeBech32mTokenIdentifier(
        identifier,
        network as NetworkType,
      );
      expect(decoded.tokenIdentifier).toEqual(TEST_TOKEN_IDENTIFIER);
      expect(decoded.network).toEqual(network);
    }
  });
});
