import { SparkSigner } from "../signer/signer.js";
import { Network, NetworkToProto } from "../utils/network.js";
import {
  ConfigOptions,
  createWalletConfigWithSigner,
  LOCAL_WALLET_CONFIG,
  MAINNET_WALLET_CONFIG,
  REGTEST_WALLET_CONFIG,
  SigningOperator,
} from "./wallet-config.js";

export class WalletConfigService {
  private readonly config: Required<ConfigOptions>;
  public readonly signer: SparkSigner;

  constructor(
    private readonly network: Network,
    options?: ConfigOptions,
  ) {
    this.config = {
      ...this.getDefaultConfig(network),
      ...options,
    };

    this.signer = this.config.signer;
  }

  private getDefaultConfig(network: Network): Required<ConfigOptions> {
    let configWithoutSigner: ConfigOptions;
    switch (network) {
      case Network.MAINNET:
        configWithoutSigner = MAINNET_WALLET_CONFIG;
        break;
      case Network.REGTEST:
        configWithoutSigner = REGTEST_WALLET_CONFIG;
        break;
      default:
        configWithoutSigner = LOCAL_WALLET_CONFIG;
        break;
    }
    return createWalletConfigWithSigner(configWithoutSigner);
  }

  public getCoordinatorAddress(): string {
    const coordinator =
      this.config.signingOperators[this.config.coodinatorIdentifier];
    if (!coordinator) {
      throw new Error(
        `Coordinator ${this.config.coodinatorIdentifier} not found`,
      );
    }
    return coordinator.address;
  }

  public getSigningOperators(): Readonly<Record<string, SigningOperator>> {
    return this.config.signingOperators;
  }

  public getThreshold(): number {
    return this.config.threshold;
  }

  public getCoordinatorIdentifier(): string {
    return this.config.coodinatorIdentifier;
  }

  public getNetwork(): Network {
    return this.network;
  }

  public getNetworkProto(): number {
    return NetworkToProto[this.network];
  }

  public shouldSignTokenTransactionsWithSchnorr(): boolean {
    return this.config.useTokenTransactionSchnorrSignatures;
  }
}
