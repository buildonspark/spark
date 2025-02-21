import * as btc from "@scure/btc-signer";
import { Network as NetworkProto } from "../proto/spark.js";

export enum Network {
  MAINNET = NetworkProto.MAINNET,
  TESTNET = NetworkProto.TESTNET,
  SIGNET = NetworkProto.SIGNET,
  REGTEST = NetworkProto.REGTEST,
  // Regtest with local signing operators
  UNRECOGNIZED = NetworkProto.UNRECOGNIZED,
}

const NetworkConfig: Record<Network, typeof btc.NETWORK> = {
  [Network.MAINNET]: btc.NETWORK,
  [Network.TESTNET]: btc.TEST_NETWORK,
  [Network.SIGNET]: btc.TEST_NETWORK,
  [Network.REGTEST]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
  [Network.UNRECOGNIZED]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
};

export const getNetwork = (network: Network): typeof btc.NETWORK =>
  NetworkConfig[network];
