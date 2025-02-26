import * as btc from "@scure/btc-signer";
import { Network as NetworkProto } from "../proto/spark.js";

export enum Network {
  MAINNET,
  TESTNET,
  SIGNET,
  REGTEST,
  LOCAL,
}

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
