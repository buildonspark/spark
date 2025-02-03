import * as btc from "@scure/btc-signer";

export type Network = "mainnet" | "testnet" | "signet" | "regtest";

const NetworkConfig: Record<Network, typeof btc.NETWORK> = {
  mainnet: btc.NETWORK,
  testnet: btc.TEST_NETWORK,
  signet: btc.TEST_NETWORK,
  regtest: { ...btc.TEST_NETWORK, bech32: "bcrt" },
};

export const getNetwork = (network: Network): typeof btc.NETWORK =>
  NetworkConfig[network];
