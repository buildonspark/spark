import { NodeHttpTransport } from "nice-grpc-web";
import { SparkWalletBrowser } from "./spark-wallet.browser.js";
import { ConfigOptions } from "../services/wallet-config.js";
import { SparkSigner } from "../signer/signer.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../services/config.js";
import { SparkWalletProps } from "./types.js";

export class SparkWalletBare extends SparkWalletBrowser {
  public static async initialize({
    mnemonicOrSeed,
    accountNumber,
    signer,
    options,
  }: SparkWalletProps) {
    const wallet = new SparkWalletBare(options, signer);
    const initResponse = await wallet.initWallet(
      mnemonicOrSeed,
      accountNumber,
      options,
    );
    return initResponse;
  }

  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, NodeHttpTransport());
  }
}

export { SparkWalletBare as SparkWallet };
