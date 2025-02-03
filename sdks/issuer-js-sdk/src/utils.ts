import { networks } from "bitcoinjs-lib";
import { NetworkType } from "@wcbd/yuv-js-sdk";

/**
 * Converts a bitcoinjs-lib Network to our NetworkType enum
 */
export function getNetworkType(network: networks.Network): NetworkType {
  if (network === networks.bitcoin) {
    return NetworkType.MAINNET;
  } else if (network === networks.testnet) {
    return NetworkType.TESTNET;
  } else if (network === networks.regtest) {
    return NetworkType.REGTEST;
  }
  throw new Error("Unsupported network type");
}
