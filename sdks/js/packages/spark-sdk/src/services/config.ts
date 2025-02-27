import { DefaultSparkSigner, SparkSigner } from "../signer/signer.js";
import {
  LOCAL_WALLET_CONFIG,
  MAINNET_WALLET_CONFIG,
  REGTEST_WALLET_CONFIG,
} from "../tests/test-util.js";
import { Network, NetworkToProto } from "../utils/network.js";

export type SigningOperator = {
  id: number;
  identifier: string;
  address: string;
  identityPublicKey: Uint8Array;
};

export type WalletConfig = {
  network: Network;
  signingOperators: Record<string, SigningOperator>;
  coodinatorIdentifier: string;
  frostSignerAddress: string;
  threshold: number;
};

export class WalletConfigService {
  private config: WalletConfig;
  public readonly signer: SparkSigner;

  constructor(network: Network, signer?: SparkSigner) {
    switch (network) {
      case Network.MAINNET:
        this.config = MAINNET_WALLET_CONFIG;
        break;
      case Network.REGTEST:
        this.config = REGTEST_WALLET_CONFIG;
        break;
      default:
        this.config = LOCAL_WALLET_CONFIG;
        break;
    }
    this.signer = signer || new DefaultSparkSigner();
  }

  getCoordinatorAddress() {
    const coordinator =
      this.config.signingOperators[this.config.coodinatorIdentifier];
    if (!coordinator) {
      throw new Error(
        `Coordinator ${this.config.coodinatorIdentifier} not found`,
      );
    }
    return coordinator.address;
  }

  getConfig() {
    return this.config;
  }

  getNetwork() {
    return this.config.network;
  }

  getNetworkProto() {
    return NetworkToProto[this.config.network];
  }
}
