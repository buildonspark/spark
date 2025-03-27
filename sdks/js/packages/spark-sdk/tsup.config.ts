import { defineConfig } from "tsup";

const commonConfig = {
  sourcemap: true,
  dts: true,
  clean: true,
  /* referenced in spark_bindings_bg.wasm: */
  // external: ["wbg"],
  entry: [
    "src/index.ts",
    "src/signer/signer.ts",
    "src/services/config.ts",
    "src/services/index.ts",
    "src/services/wallet-config.ts",
    "src/services/token-transactions.ts",
    "src/services/connection.ts",
    "src/tests/test-util.ts",
    "src/utils/index.ts",
    "src/proto/spark.ts",
    "src/graphql/objects/index.ts",
    "src/types/index.ts",
    "src/nice-grpc-web.ts",
    "src/address/index.ts",
  ],
  inject: ["./buffer.js"],
};

export default defineConfig([
  {
    ...commonConfig,
    format: ["cjs", "esm"],
    outDir: "dist",
  } /* {
  ...commonConfig,
  format: ["esm"],
  outDir: "dist/browser",
} */,
]);
