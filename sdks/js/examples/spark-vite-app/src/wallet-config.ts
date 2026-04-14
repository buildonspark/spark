import {
  createLocalSigningOperators,
  getLocalSigningThreshold,
  getSspIdentityPublicKey,
  getSspSchemaEndpoint,
  mergeConfigOptionsForNetwork,
  normalizeNetworkType,
  rewriteSigningOperatorAddresses,
  type ConfigOptions,
  type NetworkType,
} from "@buildonspark/spark-sdk";

declare const __SPARK_CONFIG_OVERRIDE__: ConfigOptions | undefined;

function readEnv(
  env: Record<string, string | undefined>,
  key: string,
): string | undefined {
  const value = env[key];
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function parsePositiveInteger(
  value: string | undefined,
  fallback: number,
): number {
  const parsed = Number.parseInt(value ?? "", 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export function getExampleSparkNetwork(
  env: Record<string, string | undefined>,
  defaultNetwork: NetworkType = "MAINNET",
): NetworkType {
  return normalizeNetworkType(
    readEnv(env, "VITE_SPARK_NETWORK") ?? getConfigOverride()?.network,
    defaultNetwork,
  );
}

export function getLocalOperatorCount(
  env: Record<string, string | undefined>,
  configOverride?: ConfigOptions,
): number {
  if (configOverride?.signingOperators) {
    return Object.keys(configOverride.signingOperators).length;
  }

  return parsePositiveInteger(readEnv(env, "VITE_NUM_SPARK_OPERATORS"), 3);
}

export function getLocalOperatorProxyPath(index: number): string {
  return `/spark-rpc/${index}`;
}

export function getLocalElectrsProxyPath(): string {
  return "/spark-electrs";
}

export function getLocalSspProxyPath(): string {
  return "/spark-ssp";
}

export function getExampleWalletOptions(
  env: Record<string, string | undefined>,
  network: NetworkType,
  browserOrigin: string,
): ConfigOptions {
  const configOverride = getConfigOverride();
  const baseOptions = mergeConfigOptionsForNetwork(network, configOverride);

  if (network !== "LOCAL") {
    return baseOptions;
  }

  const signingOperators = getLocalSigningOperatorOverrides(
    env,
    browserOrigin,
    configOverride,
  );
  const coordinatorIdentifier = Object.keys(signingOperators)[0];
  if (!coordinatorIdentifier) {
    throw new Error("Expected at least one local signing operator");
  }

  return {
    ...baseOptions,
    coordinatorIdentifier:
      baseOptions.coordinatorIdentifier ?? coordinatorIdentifier,
    electrsUrl: new URL(getLocalElectrsProxyPath(), browserOrigin).toString(),
    signingOperators,
    sspClientOptions: {
      ...baseOptions.sspClientOptions,
      baseUrl: new URL(getLocalSspProxyPath(), browserOrigin).toString(),
      identityPublicKey:
        baseOptions.sspClientOptions?.identityPublicKey ??
        getSspIdentityPublicKey("LOCAL"),
      schemaEndpoint:
        baseOptions.sspClientOptions?.schemaEndpoint ??
        getSspSchemaEndpoint("LOCAL"),
    },
    threshold:
      baseOptions.threshold ?? getLocalSigningThreshold(signingOperators),
  };
}

function getConfigOverride(): ConfigOptions | undefined {
  return typeof __SPARK_CONFIG_OVERRIDE__ === "object" &&
    __SPARK_CONFIG_OVERRIDE__ !== null
    ? __SPARK_CONFIG_OVERRIDE__
    : undefined;
}

function getLocalSigningOperatorOverrides(
  env: Record<string, string | undefined>,
  browserOrigin: string,
  configOverride?: ConfigOptions,
): NonNullable<ConfigOptions["signingOperators"]> {
  if (configOverride?.signingOperators) {
    return rewriteSigningOperatorAddresses(
      configOverride.signingOperators,
      (signingOperator) =>
        new URL(
          getLocalOperatorProxyPath(signingOperator.id),
          browserOrigin,
        ).toString(),
    );
  }

  const operatorCount = getLocalOperatorCount(env);
  return createLocalSigningOperators(
    Array.from({ length: operatorCount }, (_, index) =>
      new URL(getLocalOperatorProxyPath(index), browserOrigin).toString(),
    ),
  );
}
