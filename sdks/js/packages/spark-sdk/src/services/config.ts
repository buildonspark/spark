import { LoggingLevel, type LoggingLevelArg } from "@lightsparkdev/core";
import {
  type HasSspClientOptions,
  type SspClientOptions,
} from "../graphql/client.js";
import { BitcoinNetwork } from "../graphql/objects/BitcoinNetwork.js";
import { DefaultSparkSigner, type SparkSigner } from "../signer/signer.js";
import { Network, NetworkToProto, type NetworkType } from "../utils/network.js";
import {
  type ConfigOptions,
  LOG_SERVICE_NAMES,
  type LogConfig,
  type LogOptionsObject,
  type LogServiceName,
  type MethodLoggingConfig,
  type MethodLoggingOptions,
  type OptimizationOptions,
  type ServiceLogOptions,
  type ServiceLoggingConfig,
  type SigningOperator,
  type TokenOptimizationOptions,
  WalletConfig,
} from "./wallet-config.js";
import { SparkError } from "../errors/index.js";
import { type SparkWalletEvents } from "../spark-wallet/types.js";

function isTraceLevel(level: LoggingLevelArg | undefined): boolean {
  if (typeof level === "number") {
    return level === LoggingLevel.Trace;
  }

  return level === "TRACE" || level === "Trace" || level === "trace";
}

function normalizeLogFilePath(file: string | undefined): string | undefined {
  if (file === undefined) {
    return undefined;
  }

  const trimmedFile = file.trim();
  if (!trimmedFile) {
    throw new SparkError("log.file must be a non-empty path");
  }

  return trimmedFile;
}

function validateLogOutputOptions(
  consoleEnabled: boolean,
  file: string | undefined,
) {
  if (!consoleEnabled && file === undefined) {
    throw new SparkError("log.console can only be false when log.file is set");
  }
}

const DEFAULT_METHOD_LOGGING_CONFIG: MethodLoggingConfig = {
  enabled: false,
  collapseConsecutive: true,
  excludedMethods: [],
  exitOnly: true,
};

const DEFAULT_METHOD_LOGGING_SERVICES = new Set<LogServiceName>([
  "sparkWallet",
  "sparkReadonlyClient",
  "connectionManager",
  "sspClient",
  "transferService",
  "lightningService",
  "depositService",
  "tokenTransactionService",
]);

function cloneMethodLoggingConfig(
  config: MethodLoggingConfig = DEFAULT_METHOD_LOGGING_CONFIG,
): MethodLoggingConfig {
  return {
    enabled: config.enabled,
    collapseConsecutive: config.collapseConsecutive,
    excludedMethods: [...config.excludedMethods],
    exitOnly: config.exitOnly,
  };
}

function normalizeMethodLoggingOptions(
  options: MethodLoggingOptions | undefined,
  base: MethodLoggingConfig = DEFAULT_METHOD_LOGGING_CONFIG,
): MethodLoggingConfig {
  if (options === undefined) {
    return cloneMethodLoggingConfig(base);
  }

  if (typeof options === "boolean") {
    return {
      ...cloneMethodLoggingConfig(base),
      enabled: options,
    };
  }

  return {
    enabled: options.enabled ?? base.enabled,
    collapseConsecutive:
      options.collapseConsecutive ?? base.collapseConsecutive,
    excludedMethods: [...(options.excludedMethods ?? base.excludedMethods)],
    exitOnly: options.exitOnly ?? base.exitOnly,
  };
}

function createServiceLoggingConfig(
  serviceName: LogServiceName,
  enabled: boolean,
  level: LoggingLevelArg,
  defaultMethodLoggingEnabled: boolean,
): ServiceLoggingConfig {
  return {
    enabled,
    level,
    methods: {
      ...cloneMethodLoggingConfig(),
      enabled:
        defaultMethodLoggingEnabled &&
        DEFAULT_METHOD_LOGGING_SERVICES.has(serviceName),
    },
  };
}

function normalizeServiceLogOptions(
  options: ServiceLogOptions | null | undefined,
  base: ServiceLoggingConfig,
): ServiceLoggingConfig {
  if (options == null) {
    return base;
  }

  if (typeof options === "boolean") {
    return {
      enabled: options,
      level: base.level,
      methods: {
        ...cloneMethodLoggingConfig(base.methods),
        enabled: options,
      },
    };
  }

  const methods = normalizeMethodLoggingOptions(options.methods, base.methods);
  if (options.enabled === false && options.methods === undefined) {
    methods.enabled = false;
  }
  const hasExplicitConfig = Object.keys(options).length > 0;
  const enabled =
    methods.enabled || (options.enabled ?? (hasExplicitConfig || base.enabled));

  return {
    enabled,
    level: options.level ?? base.level,
    methods,
  };
}

export class WalletConfigService implements HasSspClientOptions {
  private readonly config: Required<ConfigOptions>;
  private readonly logOptionProvided: boolean;
  public readonly signer: SparkSigner;
  public readonly sspClientOptions: SspClientOptions;

  constructor(options: ConfigOptions = {}, signer: SparkSigner) {
    const network = options?.network ?? "REGTEST";
    this.logOptionProvided = Object.prototype.hasOwnProperty.call(
      options,
      "log",
    );

    this.config = {
      ...this.getDefaultConfig(Network[network]),
      ...options,
    };

    this.signer = signer;
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
      throw new SparkError("coordinator not found in signing operators");
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

  public getLog(): boolean {
    const services = this.getLoggingConfig().services;
    return Object.values(services).some(
      (service) => service.enabled || service.methods.enabled,
    );
  }

  public getEvents(): Partial<SparkWalletEvents> {
    return this.config.events;
  }

  public getOptimizationOptions(): OptimizationOptions {
    return this.config.optimizationOptions;
  }

  public getTokenOptimizationOptions(): TokenOptimizationOptions {
    return this.config.tokenOptimizationOptions;
  }

  public getTokenOutputLockExpiryMs(): number {
    return this.config.tokenOutputLockExpiryMs;
  }

  public getUseTokenPrimitivesBindings(): boolean {
    return this.config.useTokenPrimitivesBindings;
  }

  public getTokenTransactionVersion(): "V2" | "V3" {
    return this.config.tokenTransactionVersion;
  }

  public getLoggingLevel(): LoggingLevelArg {
    return this.getLoggingConfig().level;
  }

  public getLoggingConfig(): LogConfig {
    const logOptions = this.config.log;
    const objectOptions =
      typeof logOptions === "object" && logOptions !== null
        ? logOptions
        : undefined;
    const requestedLevel =
      typeof logOptions === "string" || typeof logOptions === "number"
        ? logOptions
        : objectOptions?.level;
    const globalLoggingEnabled =
      !this.logOptionProvided ||
      logOptions === true ||
      objectOptions !== undefined ||
      requestedLevel !== undefined;
    const defaultMethodLoggingEnabled =
      logOptions === true || isTraceLevel(requestedLevel);
    const level = logOptions === true ? "TRACE" : (requestedLevel ?? "WARN");
    const services = Object.fromEntries(
      LOG_SERVICE_NAMES.map((serviceName) => [
        serviceName,
        createServiceLoggingConfig(
          serviceName,
          globalLoggingEnabled,
          level,
          defaultMethodLoggingEnabled,
        ),
      ]),
    ) as Record<LogServiceName, ServiceLoggingConfig>;

    const baseConfig: LogConfig = {
      level: "WARN",
      timestamps: true,
      console: true,
      services,
    };

    if (
      this.logOptionProvided &&
      (logOptions === false || logOptions === undefined)
    ) {
      return baseConfig;
    }

    baseConfig.level = level;
    baseConfig.timestamps = objectOptions?.timestamps ?? baseConfig.timestamps;
    baseConfig.console = objectOptions?.console ?? baseConfig.console;
    baseConfig.file = normalizeLogFilePath(objectOptions?.file);
    validateLogOutputOptions(baseConfig.console, baseConfig.file);

    if (objectOptions?.services === "all") {
      for (const serviceName of LOG_SERVICE_NAMES) {
        baseConfig.services[serviceName] = normalizeServiceLogOptions(
          { methods: true },
          baseConfig.services[serviceName],
        );
      }

      return baseConfig;
    }

    for (const [serviceName, serviceOptions] of Object.entries(
      objectOptions?.services ?? {},
    )) {
      if (!LOG_SERVICE_NAMES.includes(serviceName as LogServiceName)) {
        continue;
      }

      baseConfig.services[serviceName as LogServiceName] =
        normalizeServiceLogOptions(
          serviceOptions,
          baseConfig.services[serviceName as LogServiceName],
        );
    }

    return baseConfig;
  }
}
