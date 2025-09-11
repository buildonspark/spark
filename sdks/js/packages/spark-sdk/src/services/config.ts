import { HasSspClientOptions, SspClientOptions } from "../graphql/client.js";
import { BitcoinNetwork } from "../graphql/objects/BitcoinNetwork.js";
import { DefaultSparkSigner, SparkSigner } from "../signer/signer.js";
import { Network, NetworkToProto, NetworkType } from "../utils/network.js";
import {
  ConfigOptions,
  WalletConfig,
  SigningOperator,
  ConsoleOptions,
} from "./wallet-config.js";
import { ConfigurationError } from "../errors/types.js";

export class WalletConfigService implements HasSspClientOptions {
  private readonly config: Required<ConfigOptions>;
  public readonly signer: SparkSigner;
  public readonly sspClientOptions: SspClientOptions;

  constructor(options?: ConfigOptions, signer?: SparkSigner) {
    const network = options?.network ?? "REGTEST";

    this.config = {
      ...this.getDefaultConfig(Network[network]),
      ...options,
    };

    this.signer = signer ?? new DefaultSparkSigner();
    this.sspClientOptions = this.config.sspClientOptions;
  }

  private getDefaultConfig(network: Network): Required<ConfigOptions> {
    switch (network) {
      case Network.MAINNET:
        return WalletConfig.MAINNET;
      case Network.REGTEST:
        return WalletConfig.REGTEST;
      default:
        return WalletConfig.LOCAL;
    }
  }

  public getCoordinatorAddress(): string {
    const coordinator =
      this.config.signingOperators[this.config.coordinatorIdentifier];
    if (!coordinator) {
      throw new ConfigurationError(
        "Coordinator not found in signing operators",
        {
          configKey: "signingOperators",
        },
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
    return this.config.coordinatorIdentifier;
  }

  public getExpectedWithdrawBondSats(): number {
    return this.config.expectedWithdrawBondSats;
  }

  public getExpectedWithdrawRelativeBlockLocktime(): number {
    return this.config.expectedWithdrawRelativeBlockLocktime;
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

  public getTokenSignatures(): "ECDSA" | "SCHNORR" {
    return this.config.tokenSignatures;
  }

  public getTokenValidityDurationSeconds(): number {
    return this.config.tokenValidityDurationSeconds;
  }

  public getElectrsUrl(): string {
    return this.config.electrsUrl;
  }

  public getSspBaseUrl(): string {
    return this.config.sspClientOptions.baseUrl;
  }

  public getSspIdentityPublicKey(): string {
    return this.config.sspClientOptions.identityPublicKey;
  }

  public getConsoleOptions(): ConsoleOptions {
    return {
      ...this.config.console,
    };
  }
}
