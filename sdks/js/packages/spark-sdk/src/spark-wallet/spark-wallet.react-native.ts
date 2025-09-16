import { XHRTransport } from "../services/xhr-transport.js";
import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConfigOptions } from "../services/wallet-config.js";
import { SparkSigner } from "../signer/signer.js";
import { ReactNativeSparkSigner } from "../signer/signer.react-native.js";
import { ConnectionManagerBrowser } from "../services/connection/connection.browser.js";
import { WalletConfigService } from "../index.node.js";
import { SparkWalletProps } from "./types.js";

export class SparkWalletReactNative extends BaseSparkWallet {
  public static async initialize({
    mnemonicOrSeed,
    accountNumber,
    signer,
    options,
  }: SparkWalletProps) {
    const wallet = new SparkWalletReactNative(options, signer);
    const initResponse = await wallet.initWallet(
      mnemonicOrSeed,
      accountNumber,
      options,
    );
    return initResponse;
  }

  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config, XHRTransport());
  }
}

export { SparkWalletReactNative as SparkWallet };
