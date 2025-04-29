import * as bitcoin from "bitcoinjs-lib";

export enum NetworkType {
  MAINNET,
  TESTNET,
  DEVNET,
  REGTEST,
  LOCAL,
}

export const networks = new Map<string, NetworkType>([
  ["MAINNET", NetworkType.MAINNET],
  ["TESTNET", NetworkType.TESTNET],
  ["DEVNET", NetworkType.DEVNET],
  ["REGTEST", NetworkType.REGTEST],
  ["LOCAL", NetworkType.LOCAL],
]);

/**
 * Convert network type to bitcoinjs-lib network.
 */
export function toPsbtNetwork(networkType: NetworkType) {
  if (networkType === NetworkType.MAINNET) {
    return bitcoin.networks.bitcoin;
  } else if (networkType === NetworkType.TESTNET) {
    return bitcoin.networks.testnet;
  } else if (networkType === NetworkType.DEVNET) {
    return bitcoin.networks.testnet;
  } else {
    // Map local to regtest network type.
    return bitcoin.networks.regtest;
  }
}

/**
 * Convert bitcoinjs-lib network to network type.
 */
export function toNetworkType(network: bitcoin.Network) {
  if (network.bech32 == bitcoin.networks.bitcoin.bech32) {
    return NetworkType.MAINNET;
  } else if (network.bech32 == bitcoin.networks.testnet.bech32) {
    return NetworkType.TESTNET;
  } else {
    return NetworkType.REGTEST;
  }
}

/**
 * Convert string network name to network type.
 */
export function getNetwork(network: string): bitcoin.Network {
  const btcNetwork = networks.get(network);
  if (!btcNetwork) {
    return bitcoin.networks.regtest;
  }

  return toPsbtNetwork(btcNetwork);
}
