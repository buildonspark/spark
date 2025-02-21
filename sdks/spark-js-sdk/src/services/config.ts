import { DefaultSparkSigner, SparkSigner } from "../signer/signer.js";
import { LOCAL_WALLET_CONFIG, REGTEST_WALLET_CONFIG } from "../tests/test-util.js";
import { Network } from "../utils/network.js";

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
    // TODO: differentiate between mainnet, regtest, and local
    // local config is LOCAL_WALLET_CONFIG - uses local signing operators
    this.config = LOCAL_WALLET_CONFIG;
    this.signer = signer || new DefaultSparkSigner();
  }

  getCoordinatorAddress() {
    return this.config.signingOperators[this.config.coodinatorIdentifier]
      .address;
  }

  getConfig() {
    return this.config;
  }

  getNetwork() {
    return this.config.network;
  }
}
