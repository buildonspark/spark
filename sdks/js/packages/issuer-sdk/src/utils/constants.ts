import { Network } from "@buildonspark/spark-sdk/utils";
import { networks } from "bitcoinjs-lib";
import { NetworkType } from "@buildonspark/lrc20-sdk";

export const LRC_WALLET_NETWORK = Object.freeze({
  [Network.MAINNET]: networks.bitcoin,
  [Network.TESTNET]: networks.testnet,
  [Network.SIGNET]: networks.testnet,
  [Network.REGTEST]: networks.regtest,
  [Network.LOCAL]: networks.regtest,
});

export const LRC_WALLET_NETWORK_TYPE = Object.freeze({
  [Network.MAINNET]: NetworkType.MAINNET,
  [Network.TESTNET]: NetworkType.TESTNET,
  [Network.SIGNET]: NetworkType.TESTNET,
  [Network.REGTEST]: NetworkType.REGTEST,
  [Network.LOCAL]: NetworkType.REGTEST,
});
