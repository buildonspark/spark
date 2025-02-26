import {Network} from "@buildonspark/spark-sdk/utils";
import {networks} from "bitcoinjs-lib";
import lrc20sdk from "@buildonspark/lrc20-sdk";

export const LRC_WALLET_NETWORK = Object.freeze({
  [Network.MAINNET]: networks.bitcoin,
  [Network.TESTNET]: networks.testnet,
  [Network.SIGNET]: networks.testnet,
  [Network.REGTEST]: networks.regtest,
  [Network.LOCAL]: networks.regtest,
});

export const LRC_WALLET_NETWORK_TYPE = Object.freeze({
  [Network.MAINNET]: lrc20sdk.NetworkType.MAINNET,
  [Network.TESTNET]: lrc20sdk.NetworkType.TESTNET,
  [Network.SIGNET]: lrc20sdk.NetworkType.TESTNET,
  [Network.REGTEST]: lrc20sdk.NetworkType.LS_REGTEST,
  [Network.LOCAL]: lrc20sdk.NetworkType.REGTEST,
});
