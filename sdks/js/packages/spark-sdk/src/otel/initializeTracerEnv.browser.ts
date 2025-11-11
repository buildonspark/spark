import { type SparkWallet as BaseSparkWallet } from "../spark-wallet/spark-wallet.browser.js";
import { WebTracerProvider } from "@opentelemetry/sdk-trace-web";
import { registerInstrumentations } from "@opentelemetry/instrumentation";
import { FetchInstrumentation } from "@opentelemetry/instrumentation-fetch";
import { W3CTraceContextPropagator } from "@opentelemetry/core";
import { propagation } from "@opentelemetry/api";

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

export { initializeTracerEnvBrowser as initializeTracerEnv };
