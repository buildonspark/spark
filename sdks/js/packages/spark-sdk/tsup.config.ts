import { defineConfig } from "tsup";

const commonConfig = {
  sourcemap: true,
  dts: true,
  clean: false,
  inject: ["./buffer.js"],
};

export default defineConfig([
  {
    ...commonConfig,
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
      "src/address/index.ts",
    ],
    format: ["cjs", "esm"],
    outDir: "dist",
  },
  {
    ...commonConfig,
    entry: ["src/native/index.ts"],
    format: ["cjs", "esm"],
    banner: {
      /* @noble/hashes assigns crypto export on module load which makes it sensitive to
          module load order. As a result crypto needs to be available when it first loads.
          esbuild inject does not guarentee the injected module will be loaded first,
          so we need to leverage banner for this. An alternative to may be to wrap any imports
          of @noble/hashes (and other deps that import it like some @scure imports do) in local modules,
          and import react-native-get-random-values first in those modules. */
      js: `require("react-native-get-random-values");`,
    },
    outDir: "dist/native",
  },
]);
