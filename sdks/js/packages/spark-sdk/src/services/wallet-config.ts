import { hexToBytes } from "@noble/curves/abstract/utils";
import { NetworkType } from "../utils/network.js";

export type SigningOperator = {
  readonly id: number;
  readonly identifier: string;
  readonly address: string;
  readonly identityPublicKey: Uint8Array;
};

export type ConfigOptions = {
  readonly network?: NetworkType;
  readonly signingOperators?: Readonly<Record<string, SigningOperator>>;
  readonly coodinatorIdentifier?: string;
  readonly frostSignerAddress?: string;
  readonly lrc20Address?: string;
  readonly threshold?: number;
  readonly useTokenTransactionSchnorrSignatures?: boolean;
};

const BASE_CONFIG: Required<ConfigOptions> = {
  network: "LOCAL",
  lrc20Address: "http://127.0.0.1:18530",
  coodinatorIdentifier:
    "0000000000000000000000000000000000000000000000000000000000000001",
  frostSignerAddress: "unix:///tmp/frost_0.sock",
  threshold: 2,
  signingOperators: getLocalSigningOperators(),
  useTokenTransactionSchnorrSignatures: true,
};

export const LOCAL_WALLET_CONFIG: Required<ConfigOptions> = {
  ...BASE_CONFIG,
};

export const LOCAL_WALLET_CONFIG_SCHNORR: Required<ConfigOptions> = {
  ...BASE_CONFIG,
};

export const LOCAL_WALLET_CONFIG_ECDSA: Required<ConfigOptions> = {
  ...BASE_CONFIG,
  useTokenTransactionSchnorrSignatures: false,
};

export const REGTEST_WALLET_CONFIG: Required<ConfigOptions> = {
  ...BASE_CONFIG,
  network: "REGTEST",
  lrc20Address: "https://regtest.lrc20.dev.dev.sparkinfra.net:443",
  signingOperators: getRegtestSigningOperators(),
};

export const MAINNET_WALLET_CONFIG: Required<ConfigOptions> = {
  ...BASE_CONFIG,
  network: "MAINNET",
  lrc20Address: "https://mainnet.lrc20.dev.dev.sparkinfra.net:443",
  signingOperators: getRegtestSigningOperators(),
};

export function getRegtestSigningOperators(): Record<string, SigningOperator> {
  return {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      address: "https://0.spark.dev.dev.sparkinfra.net",

      identityPublicKey: hexToBytes(
        "03dfbdff4b6332c220f8fa2ba8ed496c698ceada563fa01b67d9983bfc5c95e763",
      ),
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "https://1.spark.dev.dev.sparkinfra.net",

      identityPublicKey: hexToBytes(
        "03e625e9768651c9be268e287245cc33f96a68ce9141b0b4769205db027ee8ed77",
      ),
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "https://2.spark.dev.dev.sparkinfra.net",
      identityPublicKey: hexToBytes(
        "022eda13465a59205413086130a65dc0ed1b8f8e51937043161f8be0c369b1a410",
      ),
    },
  };
}

export function getLocalSigningOperators(): Record<string, SigningOperator> {
  const pubkeys = [
    "0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510",
    "0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27",
    "0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6",
    "0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea",
    "02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba",
  ];

  const pubkeyBytesArray = pubkeys.map((pubkey) => hexToBytes(pubkey));

  return {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      address: "https://localhost:8535",
      identityPublicKey: pubkeyBytesArray[0]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "https://localhost:8536",
      identityPublicKey: pubkeyBytesArray[1]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "https://localhost:8537",
      identityPublicKey: pubkeyBytesArray[2]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000004": {
      id: 3,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000004",
      address: "https://localhost:8538",
      identityPublicKey: pubkeyBytesArray[3]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000005": {
      id: 4,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000005",
      address: "https://localhost:8539",
      identityPublicKey: pubkeyBytesArray[4]!,
    },
  };
}
