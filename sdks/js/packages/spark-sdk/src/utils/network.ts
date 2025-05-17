import { NetworkType as Lrc20NetworkType } from "@buildonspark/lrc20-sdk";
import * as btc from "@scure/btc-signer";
import * as bitcoin from "bitcoinjs-lib";
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

const NetworkConfig: Record<Network, typeof btc.NETWORK> = {
  [Network.MAINNET]: btc.NETWORK,
  [Network.TESTNET]: btc.TEST_NETWORK,
  [Network.SIGNET]: btc.TEST_NETWORK,
  [Network.REGTEST]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
  [Network.LOCAL]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
};

export const getNetwork = (network: Network): typeof btc.NETWORK =>
  NetworkConfig[network];

export const LRC_WALLET_NETWORK = Object.freeze({
  [Network.MAINNET]: bitcoin.networks.bitcoin,
  [Network.TESTNET]: bitcoin.networks.testnet,
  [Network.SIGNET]: bitcoin.networks.testnet,
  [Network.REGTEST]: bitcoin.networks.regtest,
  [Network.LOCAL]: bitcoin.networks.regtest,
});

export const LRC_WALLET_NETWORK_TYPE = Object.freeze({
  [Network.MAINNET]: Lrc20NetworkType.MAINNET,
  [Network.TESTNET]: Lrc20NetworkType.TESTNET,
  [Network.SIGNET]: Lrc20NetworkType.TESTNET,
  [Network.REGTEST]: Lrc20NetworkType.REGTEST,
  [Network.LOCAL]: Lrc20NetworkType.LOCAL,
});

/**
 * Utility function to determine the network from a Bitcoin address.
 *
 * @param {string} address - The Bitcoin address
 * @returns {BitcoinNetwork | null} The detected network or null if not detected
 */
export function getNetworkFromAddress(address: string) {
  try {
    const decoded = bitcoin.address.fromBech32(address);
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
