import {
  HasLrc20WalletApiConfig,
  LRC20WalletApiConfig,
} from "@buildonspark/lrc20-sdk";
import { HasSspClientOptions, SspClientOptions } from "../graphql/client.js";
import { BitcoinNetwork } from "../graphql/objects/BitcoinNetwork.js";
import { DefaultSparkSigner, SparkSigner } from "../signer/signer.js";
import { Network, NetworkToProto, NetworkType } from "../utils/network.js";
import {
  ConfigOptions,
  LOCAL_WALLET_CONFIG,
  MAINNET_WALLET_CONFIG,
  REGTEST_WALLET_CONFIG,
  SigningOperator,
} from "./wallet-config.js";

export class WalletConfigService
  implements HasLrc20WalletApiConfig, HasSspClientOptions
{
  private readonly config: Required<ConfigOptions>;
  public readonly signer: SparkSigner;
  public readonly lrc20ApiConfig: LRC20WalletApiConfig;
  public readonly sspClientOptions: SspClientOptions;

  constructor(options?: ConfigOptions, signer?: SparkSigner) {
    const network = options?.network ?? "REGTEST";

    this.config = {
      ...this.getDefaultConfig(Network[network]),
      ...options,
    };

    this.signer = signer ?? new DefaultSparkSigner();
    this.lrc20ApiConfig = this.config.lrc20ApiConfig;
    this.sspClientOptions = this.config.sspClientOptions;
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

  public getLrc20Address(): string {
    return this.config.lrc20Address;
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

  public getSspNetwork(): BitcoinNetwork {
    if (this.config.network === "MAINNET") {
      return BitcoinNetwork.MAINNET;
    } else if (this.config.network === "REGTEST") {
      return BitcoinNetwork.REGTEST;
    } else if (this.config.network === "TESTNET") {
      return BitcoinNetwork.TESTNET;
    } else if (this.config.network === "SIGNET") {
      return BitcoinNetwork.SIGNET;
    }
    return BitcoinNetwork.FUTURE_VALUE;
  }

  public getNetwork(): Network {
    return Network[this.config.network];
  }

  public getNetworkType(): NetworkType {
    return this.config.network;
  }

  public getNetworkProto(): number {
    return NetworkToProto[Network[this.config.network]];
  }

  public shouldSignTokenTransactionsWithSchnorr(): boolean {
    return this.config.useTokenTransactionSchnorrSignatures;
  }

  public getElectrsUrl(): string {
    return this.config.electrsUrl;
  }

  public getSspIdentityPublicKey(): string {
    return this.config.sspClientOptions.identityPublicKey;
  }
}
