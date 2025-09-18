import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { WebTracerProvider } from "@opentelemetry/sdk-trace-web";
import { registerInstrumentations } from "@opentelemetry/instrumentation";
import { FetchInstrumentation } from "@opentelemetry/instrumentation-fetch";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { propagation } from "@opentelemetry/api";
import {
  ConnectionManagerBrowser,
  type Transport,
} from "../services/connection/connection.browser.js";
import { SparkSigner } from "../signer/signer.js";
import { ConfigOptions } from "../services/wallet-config.js";
import { WalletConfigService } from "../services/config.js";

export class SparkWalletBrowser extends BaseSparkWallet {
  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerBrowser(config);
  }

  protected initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
    initializeTracerEnvBrowser({ spanProcessors, traceUrls });
  }
}

export function initializeTracerEnvBrowser({
  spanProcessors,
  traceUrls,
}: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
  const provider = new WebTracerProvider({ spanProcessors });
  provider.register();

  propagation.setGlobalPropagator(new W3CTraceContextPropagator());

  registerInstrumentations({
    instrumentations: [
      new FetchInstrumentation({
        ignoreUrls: [
          /* Since we're wrapping global fetch we should be careful to avoid
             adding headers for unrelated requests */
          new RegExp(
            `^(?!(${traceUrls
              .map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"))
              .join("|")}))`,
          ),
        ],
        propagateTraceHeaderCorsUrls: /.*/,
      }),
    ],
  });
}

export {
  SparkWalletBrowser as SparkWallet,
  initializeTracerEnvBrowser as initializeTracerEnv,
};
