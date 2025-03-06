import { initEccLib, networks } from "bitcoinjs-lib";
import { NetworkType } from "../../network";

// Secp256k1 base point.
export const G = Buffer.from("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798", "hex");

export const network = networks.regtest;

export const TOKEN_AMOUNT_SIZE = 32;
export const BLINDING_FACTOR_SIZE = 16;
export const MIN_DUST_AMOUNT = 1000;
export const DUST_AMOUNT = 300;

export const PARITY = Buffer.from([2]);
export const EMPTY_TOKEN_PUBKEY = Buffer.from(Array(33).fill(2));

export const ESPLORA_URL = Object.freeze({
  [NetworkType.MAINNET]: "https://mempool.space/api",
  [NetworkType.REGTEST]: "http://127.0.0.1:30000",
  [NetworkType.TESTNET]: "https://electrs.mutiny.18.215.149.26.sslip.io",
  [NetworkType.DEVNET]: "https://electrs.stage.18.215.149.26.sslip.io",
  [NetworkType.LS_REGTEST]: "https://regtest-mempool.dev.dev.sparkinfra.net/api",
  default: "https://mutinynet.com/api",
});

export const ESPLORA_AUTH = Object.freeze({
  [NetworkType.LS_REGTEST]: {
    username: "distributedlabs",
    password: "bV5mT8pL4xH2nQ9j",
  },
  default: null,
});

export const LRC_NODE_URL = Object.freeze({
  [NetworkType.MAINNET]: "http://54.219.77.43:18333",
  [NetworkType.TESTNET]: "https://rpc.lrc20d.mutiny.18.215.149.26.sslip.io",
  [NetworkType.DEVNET]: "https://rpc.lrc20.stage.18.215.149.26.sslip.io",
  [NetworkType.LS_REGTEST]: "https://regtest.lrc20.dev.dev.sparkinfra.net",
  default: "http://127.0.0.1:18333",
});
