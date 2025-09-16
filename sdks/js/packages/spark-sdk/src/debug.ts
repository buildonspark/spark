import { SparkWallet } from "./spark-wallet/spark-wallet.browser.js";
import { WalletConfig } from "./services/wallet-config.js";
import * as utils from "./utils/index.js";

const s = {
  SparkWallet,
  WalletConfig,
  utils,
};

globalThis.s = s;

export { s };
