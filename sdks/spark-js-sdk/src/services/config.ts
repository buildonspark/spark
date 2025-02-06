import { DefaultSparkSigner, SparkSigner } from "../signer/signer";
import { getAllSigningOperators } from "../tests/test-util";
import { Network } from "../utils/network";

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

  // TODO: update config based on network
  constructor(network: Network, signer?: SparkSigner) {
    this.config = {
      network,
      coodinatorIdentifier:
        "0000000000000000000000000000000000000000000000000000000000000001",
      frostSignerAddress: "unix:///tmp/frost_0.sock",
      threshold: 3,
      signingOperators: getAllSigningOperators(),
    };
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
