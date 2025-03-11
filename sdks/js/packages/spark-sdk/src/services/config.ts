import { SparkSigner } from "../signer/signer.js";
import {
  LOCAL_WALLET_CONFIG,
  MAINNET_WALLET_CONFIG,
  REGTEST_WALLET_CONFIG,
  ConfigOptions,
  SigningOperator,
} from "./wallet-config.js";
import { Network, NetworkToProto } from "../utils/network.js";

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
    switch (network) {
      case Network.MAINNET:
        return MAINNET_WALLET_CONFIG;
      case Network.REGTEST:
        return REGTEST_WALLET_CONFIG;
      default:
        return LOCAL_WALLET_CONFIG;
    }
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
