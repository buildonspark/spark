import * as btc from "@scure/btc-signer";
import { bech32, bech32m } from "@scure/base";
import { Network as NetworkProto } from "../proto/spark.js";
import { BitcoinNetwork } from "../graphql/objects/BitcoinNetwork.js";
import { ValidationError } from "../errors/index.js";

export enum Network {
  MAINNET,
  TESTNET,
  SIGNET,
  REGTEST,
  LOCAL,
}

export type NetworkType = keyof typeof Network;

export const NetworkToProto: Record<Network, NetworkProto> = {
  [Network.MAINNET]: NetworkProto.MAINNET,
  [Network.TESTNET]: NetworkProto.TESTNET,
  [Network.SIGNET]: NetworkProto.SIGNET,
  [Network.REGTEST]: NetworkProto.REGTEST,
  [Network.LOCAL]: NetworkProto.REGTEST,
};

export const protoToNetwork = (
  protoNetwork: NetworkProto,
): Network | undefined => {
  switch (protoNetwork) {
    case NetworkProto.MAINNET:
      return Network.MAINNET;
    case NetworkProto.TESTNET:
      return Network.TESTNET;
    case NetworkProto.SIGNET:
      return Network.SIGNET;
    case NetworkProto.REGTEST:
      return Network.REGTEST;
    default:
      return undefined;
  }
};

const NetworkConfig: Record<Network, typeof btc.NETWORK> = {
  [Network.MAINNET]: btc.NETWORK,
  [Network.TESTNET]: btc.TEST_NETWORK,
  [Network.SIGNET]: btc.TEST_NETWORK,
  [Network.REGTEST]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
  [Network.LOCAL]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
};

export const getNetwork = (network: Network): typeof btc.NETWORK =>
  NetworkConfig[network];

/**
 * Utility function to determine the network from a Bitcoin address.
 *
 * @param {string} address - The Bitcoin address
 * @returns {BitcoinNetwork | null} The detected network or null if not detected
 */
export function getNetworkFromAddress(address: string) {
  try {
    // Try bech32 first, then bech32m (Taproot)
    const bechAddress = address as `${string}1${string}`;
    const decoded = (() => {
      try {
        return bech32.decode(bechAddress);
      } catch (_) {
        return bech32m.decode(bechAddress);
      }
    })();

    // HRP (human-readable part) determines the network
    if (decoded.prefix === "bc") {
      return BitcoinNetwork.MAINNET;
    } else if (decoded.prefix === "bcrt") {
      return BitcoinNetwork.REGTEST;
    }
  } catch (err) {
    throw new ValidationError(
      "Invalid Bitcoin address",
      {
        field: "address",
        value: address,
        expected: "Valid Bech32 address with prefix 'bc' or 'bcrt'",
      },
      err instanceof Error ? err : undefined,
    );
  }
  return null;
}

/**
 * Utility function to get the network enum value from a string.
 *
 * @param {string} network - The Bitcoin network to turn into an enum value
 * @returns {Network} The enum value matching the string
 */
export function getNetworkFromString(network?: string) {
  const net = (network ?? "REGTEST").toUpperCase();
  if (net === "MAINNET") return Network.MAINNET;
  if (net === "TESTNET") return Network.TESTNET;
  if (net === "SIGNET") return Network.SIGNET;
  if (net === "LOCAL") return Network.LOCAL;
  return Network.REGTEST; // default
}
