import { networks } from "bitcoinjs-lib";
import { Buffer } from "buffer";
import { NetworkType } from "../../network/index.ts";

// Secp256k1 base point.
export const G = Buffer.from("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", "hex");

export const network = networks.regtest;

export const TOKEN_AMOUNT_SIZE = 32;
export const BLINDING_FACTOR_SIZE = 16;
export const MIN_DUST_AMOUNT = 1000;
export const DUST_AMOUNT = 354;

export const PARITY = Buffer.from([2]);
export const EMPTY_TOKEN_PUBKEY = Buffer.from(Array(33).fill(2));

export const ELECTRS_URL = Object.freeze({
  [NetworkType.MAINNET]: "https://mempool.space/api",
  [NetworkType.REGTEST]: "https://regtest-mempool.us-west-2.sparkinfra.net/api",
  [NetworkType.TESTNET]: "https://electrs.mutiny.18.215.149.26.sslip.io",
  [NetworkType.DEVNET]: "https://electrs.stage.18.215.149.26.sslip.io",
  [NetworkType.LOCAL]: "http://127.0.0.1:30000",
  default: "http://127.0.0.1:30000",
});

export const LRC_NODE_URL = Object.freeze({
  [NetworkType.MAINNET]: "https://mainnet.lrc20.us-west-2.sparkinfra.net",
  [NetworkType.REGTEST]: "https://regtest.lrc20.us-west-2.sparkinfra.net",
  [NetworkType.TESTNET]: "https://rpc.lrc20d.mutiny.18.215.149.26.sslip.io",
  [NetworkType.DEVNET]: "https://rpc.lrc20.stage.18.215.149.26.sslip.io",
  [NetworkType.LOCAL]: "http://127.0.0.1:18332",
  default: "http://127.0.0.1:18332",
});
