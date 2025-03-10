import { NetworkType as Lrc20NetworkType } from "@buildonspark/lrc20-sdk";
import * as btc from "@scure/btc-signer";
import { networks } from "bitcoinjs-lib";
import { Network as NetworkProto } from "../proto/spark.js";
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
  [Network.MAINNET]: networks.bitcoin,
  [Network.TESTNET]: networks.testnet,
  [Network.SIGNET]: networks.testnet,
  [Network.REGTEST]: networks.regtest,
  [Network.LOCAL]: networks.regtest,
});

export const LRC_WALLET_NETWORK_TYPE = Object.freeze({
  [Network.MAINNET]: Lrc20NetworkType.MAINNET,
  [Network.TESTNET]: Lrc20NetworkType.TESTNET,
  [Network.SIGNET]: Lrc20NetworkType.TESTNET,
  [Network.REGTEST]: Lrc20NetworkType.REGTEST,
  [Network.LOCAL]: Lrc20NetworkType.REGTEST,
});
