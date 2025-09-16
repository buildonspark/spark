import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { AsyncLocalStorageContextManager } from "@opentelemetry/context-async-hooks";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { registerInstrumentations } from "@opentelemetry/instrumentation";
import { UndiciInstrumentation } from "@opentelemetry/instrumentation-undici";
import { SparkWalletProps } from "./types.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletNodeJS extends BaseSparkWallet {
  public static async initialize({
    mnemonicOrSeed,
    accountNumber,
    signer,
    options,
  }: SparkWalletProps) {
    const wallet = new SparkWalletNodeJS(options, signer);
    const initResponse = await wallet.initWallet(
      mnemonicOrSeed,
      accountNumber,
      options,
    );
    return initResponse;
  }

  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerNodeJS(config);
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
    initializeTracerEnvNodeJS({ spanProcessors, traceUrls });
  }
}

export function initializeTracerEnvNodeJS({
  spanProcessors,
  traceUrls,
}: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
  const provider = new NodeTracerProvider({ spanProcessors });
  provider.register({
    contextManager: new AsyncLocalStorageContextManager(),
    propagator: new W3CTraceContextPropagator(),
  });

  registerInstrumentations({
    instrumentations: [
      new UndiciInstrumentation({
        ignoreRequestHook: (request) => {
          /* Since we're wrapping global fetch we should be careful to avoid
               adding headers or causing errors for unrelated requests */
          try {
            return !traceUrls.some((prefix) =>
              request.origin.startsWith(prefix),
            );
          } catch {
            return true;
          }
        },
      }),
    ],
  });
}

export {
  SparkWalletNodeJS as SparkWallet,
  initializeTracerEnvNodeJS as initializeTracerEnv,
};
