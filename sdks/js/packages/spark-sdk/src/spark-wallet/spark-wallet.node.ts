import { SparkWallet as BaseSparkWallet } from "./spark-wallet.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { WalletConfigService } from "../services/config.js";
import { trace } from "@opentelemetry/api";
import { initializeTracerEnvNodeJS } from "../otel/initializeTracerEnv.node.js";

export class SparkWalletNodeJS extends BaseSparkWallet {
  protected override getTracer() {
    /* OTEL tracing is only supported on Node.js, see LIG-8409 */
    if (!this.tracer) {
      this.tracer = trace.getTracer(this.tracerId);
    }
    return this.tracer;
  }

  protected buildConnectionManager(config: WalletConfigService) {
    return new ConnectionManagerNodeJS(config);
  }

  protected override initializeTracerEnv({
    spanProcessors,
    traceUrls,
  }: Parameters<BaseSparkWallet["initializeTracerEnv"]>[0]) {
    initializeTracerEnvNodeJS({ spanProcessors, traceUrls });
  }
}

export { SparkWalletNodeJS as SparkWallet };
