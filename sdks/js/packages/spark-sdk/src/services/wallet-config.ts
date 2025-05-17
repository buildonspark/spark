import {
  LRC20WalletApiConfig,
  MayHaveLrc20WalletApiConfig,
} from "@buildonspark/lrc20-sdk";
import {
  MayHaveSspClientOptions,
  SspClientOptions,
} from "../graphql/client.js";
import { NetworkType } from "../utils/network.js";
import { isHermeticTest } from "../tests/isHermeticTest.js";

const SSP_IDENTITY_PUBLIC_KEYS = {
  LOCAL: "028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4",
  REGTEST: {
    PROD: "022bf283544b16c0622daecb79422007d167eca6ce9f0c98c0c49833b1f7170bfe",
  },
  MAINNET: {
    PROD: "023e33e2920326f64ea31058d44777442d97d7d5cbfcf54e3060bc1695e5261c93",
  },
};

const URL_CONFIG = {
  LOCAL: {
    SSP: "http://127.0.0.1:5000",
    ELECTRS: "http://127.0.0.1:30000",
    LRC20: "http://127.0.0.1:18530",
    LRC20_NODE: "http://127.0.0.1:18332",
  },
  REGTEST: {
    PROD: {
      SSP: "https://api.lightspark.com",
      ELECTRS: "https://regtest-mempool.us-west-2.sparkinfra.net/api",
      LRC20: "https://regtest.lrc20.lightspark.com:443",
      LRC20_NODE: "https://regtest.lrc20.lightspark.com",
    },
  },
  MAINNET: {
    PROD: {
      SSP: "https://api.lightspark.com",
      ELECTRS: "https://mempool.space/api",
      LRC20: "https://mainnet.lrc20.lightspark.com:443",
      LRC20_NODE: "https://mainnet.lrc20.lightspark.com",
    },
  },
} as const;

export const ELECTRS_CREDENTIALS = {
  username: "spark-sdk",
  password: "mCMk1JqlBNtetUNy",
};

export function getElectrsUrl(network: NetworkType): string {
  switch (network) {
    case "LOCAL":
      return isHermeticTest()
        ? "http://mempool.minikube.local/api"
        : URL_CONFIG.LOCAL.ELECTRS;
    case "REGTEST":
      return URL_CONFIG.REGTEST.PROD.ELECTRS;
    case "MAINNET":
      return URL_CONFIG.MAINNET.PROD.ELECTRS;
    default:
      return URL_CONFIG.LOCAL.ELECTRS;
  }
}

export function getLrc20Url(network: NetworkType): string {
  switch (network) {
    case "LOCAL":
      return URL_CONFIG.LOCAL.LRC20;
    case "REGTEST":
      return URL_CONFIG.REGTEST.PROD.LRC20;
    case "MAINNET":
      return URL_CONFIG.MAINNET.PROD.LRC20;
    default:
      return URL_CONFIG.LOCAL.LRC20;
  }
}

export function getLrc20NodeUrl(network: NetworkType): string {
  switch (network) {
    case "LOCAL":
      return URL_CONFIG.LOCAL.LRC20_NODE;
    case "REGTEST":
      return URL_CONFIG.REGTEST.PROD.LRC20_NODE;
    case "MAINNET":
      return URL_CONFIG.MAINNET.PROD.LRC20_NODE;
    default:
      return URL_CONFIG.LOCAL.LRC20_NODE;
  }
}

export function getSspIdentityPublicKey(network: NetworkType): string {
  switch (network) {
    case "LOCAL":
      return SSP_IDENTITY_PUBLIC_KEYS.LOCAL;
    case "REGTEST":
      return SSP_IDENTITY_PUBLIC_KEYS.REGTEST.PROD;
    case "MAINNET":
      return SSP_IDENTITY_PUBLIC_KEYS.MAINNET.PROD;
    default:
      return SSP_IDENTITY_PUBLIC_KEYS.LOCAL;
  }
}

export function getSspUrl(network: NetworkType): string {
  switch (network) {
    case "LOCAL":
      return isHermeticTest()
        ? "http://app.minikube.local"
        : URL_CONFIG.LOCAL.SSP;
    case "REGTEST":
      return URL_CONFIG.REGTEST.PROD.SSP;
    case "MAINNET":
      return URL_CONFIG.MAINNET.PROD.SSP;
    default:
      return URL_CONFIG.LOCAL.SSP;
  }
}

export function getSspSchemaEndpoint(network: NetworkType): string | undefined {
  switch (network) {
    case "LOCAL":
      return "graphql/spark/rc";
  }
  return;
}
export type SigningOperator = {
  readonly id: number;
  readonly identifier: string;
  readonly address: string;
  readonly identityPublicKey: string;
};

export type ConfigOptions = MayHaveLrc20WalletApiConfig &
  MayHaveSspClientOptions & {
    readonly network?: NetworkType;
    readonly signingOperators?: Readonly<Record<string, SigningOperator>>;
    readonly coodinatorIdentifier?: string;
    readonly frostSignerAddress?: string;
    readonly lrc20Address?: string;
    readonly threshold?: number;
    readonly useTokenTransactionSchnorrSignatures?: boolean;
    readonly electrsUrl?: string;
    readonly lrc20ApiConfig?: LRC20WalletApiConfig;
    readonly sspClientOptions?: SspClientOptions;
    readonly expectedWithdrawBondSats?: number;
    readonly expectedWithdrawRelativeBlockLocktime?: number;
  };

const DEV_PUBKEYS = [
  "03acd9a5a88db102730ff83dee69d69088cc4c9d93bbee893e90fd5051b7da9651",
  "02d2d103cacb1d6355efeab27637c74484e2a7459e49110c3fe885210369782e23",
  "0350f07ffc21bfd59d31e0a7a600e2995273938444447cb9bc4c75b8a895dbb853",
];

const PROD_PUBKEYS = [
  "03dfbdff4b6332c220f8fa2ba8ed496c698ceada563fa01b67d9983bfc5c95e763",
  "03e625e9768651c9be268e287245cc33f96a68ce9141b0b4769205db027ee8ed77",
  "022eda13465a59205413086130a65dc0ed1b8f8e51937043161f8be0c369b1a410",
];

function getLocalFrostSignerAddress(): string {
  return isHermeticTest() ? "localhost:9999" : "unix:///tmp/frost_0.sock";
}

const BASE_CONFIG: Required<ConfigOptions> = {
  network: "LOCAL",
  lrc20Address: getLrc20Url("LOCAL"),
  coodinatorIdentifier:
    "0000000000000000000000000000000000000000000000000000000000000001",
  frostSignerAddress: getLocalFrostSignerAddress(),
  threshold: 2,
  signingOperators: getLocalSigningOperators(),
  useTokenTransactionSchnorrSignatures: true,
  electrsUrl: getElectrsUrl("LOCAL"),
  expectedWithdrawBondSats: 10000,
  expectedWithdrawRelativeBlockLocktime: 1000,
  lrc20ApiConfig: {
    electrsUrl: getElectrsUrl("LOCAL"),
    lrc20NodeUrl: getLrc20NodeUrl("LOCAL"),
    electrsCredentials: ELECTRS_CREDENTIALS,
  },
  sspClientOptions: {
    baseUrl: getSspUrl("LOCAL"),
    identityPublicKey: getSspIdentityPublicKey("LOCAL"),
    schemaEndpoint: getSspSchemaEndpoint("LOCAL"),
  },
};

export const LOCAL_WALLET_CONFIG: Required<ConfigOptions> = {
  ...BASE_CONFIG,
};

export const LOCAL_WALLET_CONFIG_SCHNORR: Required<ConfigOptions> = {
  ...LOCAL_WALLET_CONFIG,
  threshold: 3, // 3 for issuance tests.
};

export const LOCAL_WALLET_CONFIG_ECDSA: Required<ConfigOptions> = {
  ...LOCAL_WALLET_CONFIG,
  useTokenTransactionSchnorrSignatures: false,
  threshold: 3, // 3 for issuance tests.
};

export const REGTEST_WALLET_CONFIG: Required<ConfigOptions> = {
  ...BASE_CONFIG,
  network: "REGTEST",
  lrc20Address: getLrc20Url("REGTEST"),
  signingOperators: getSigningOperators(),
  electrsUrl: getElectrsUrl("REGTEST"),
  lrc20ApiConfig: {
    electrsUrl: getElectrsUrl("REGTEST"),
    lrc20NodeUrl: getLrc20NodeUrl("REGTEST"),
    electrsCredentials: ELECTRS_CREDENTIALS,
  },
  expectedWithdrawBondSats: 10000,
  expectedWithdrawRelativeBlockLocktime: 1000,
  sspClientOptions: {
    baseUrl: getSspUrl("REGTEST"),
    identityPublicKey: getSspIdentityPublicKey("REGTEST"),
  },
};

export const MAINNET_WALLET_CONFIG: Required<ConfigOptions> = {
  ...BASE_CONFIG,
  network: "MAINNET",
  lrc20Address: getLrc20Url("MAINNET"),
  signingOperators: getSigningOperators(),
  electrsUrl: getElectrsUrl("MAINNET"),
  lrc20ApiConfig: {
    electrsUrl: getElectrsUrl("MAINNET"),
    lrc20NodeUrl: getLrc20NodeUrl("MAINNET"),
  },
  expectedWithdrawBondSats: 10000,
  expectedWithdrawRelativeBlockLocktime: 1000,
  sspClientOptions: {
    baseUrl: getSspUrl("MAINNET"),
    identityPublicKey: getSspIdentityPublicKey("MAINNET"),
  },
};

function getSigningOperators(): Record<string, SigningOperator> {
  return {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      address: "https://0.spark.lightspark.com",
      identityPublicKey: PROD_PUBKEYS[0]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "https://1.spark.lightspark.com",
      identityPublicKey: PROD_PUBKEYS[1]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "https://2.spark.flashnet.xyz",
      identityPublicKey: PROD_PUBKEYS[2]!,
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

  return {
    "0000000000000000000000000000000000000000000000000000000000000001": {
      id: 0,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      address: "https://localhost:8535",
      identityPublicKey: pubkeys[0]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000002": {
      id: 1,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000002",
      address: "https://localhost:8536",
      identityPublicKey: pubkeys[1]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000003": {
      id: 2,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000003",
      address: "https://localhost:8537",
      identityPublicKey: pubkeys[2]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000004": {
      id: 3,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000004",
      address: "https://localhost:8538",
      identityPublicKey: pubkeys[3]!,
    },
    "0000000000000000000000000000000000000000000000000000000000000005": {
      id: 4,
      identifier:
        "0000000000000000000000000000000000000000000000000000000000000005",
      address: "https://localhost:8539",
      identityPublicKey: pubkeys[4]!,
    },
  };
}
