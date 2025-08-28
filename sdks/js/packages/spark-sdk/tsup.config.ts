import { readFileSync } from "node:fs";
import { defineConfig } from "tsup";

const pkg = JSON.parse(
  readFileSync(new URL("./package.json", import.meta.url), "utf8"),
);

const commonConfig = {
  sourcemap: false,
  dts: true,
  clean: false,
  define: {
    __PACKAGE_VERSION__: JSON.stringify(pkg.version),
  },
};

export default defineConfig([
  {
    ...commonConfig,
    entry: [
      "src/index.ts",
      "src/index.node.ts",
      /* Entrypoints other than index should be static only, i.e. modules that never depend
         on the state of other modules. Everything else should be exported from index. */
      "src/tests/test-utils.ts",
      "src/debug.ts",
      "src/proto/spark.ts",
      "src/proto/spark_token.ts",
      "src/graphql/objects/index.ts",
      "src/types/index.ts",
      "src/spark_bindings/wasm/index.ts",
      "src/spark_bindings/native/index.ts",
    ],
    inject: ["./buffer.js"],
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
          esbuild inject does not guarantee the injected module will be loaded first,
          so we need to leverage banner for this. An alternative to may be to wrap any imports
          of @noble/hashes (and other deps that import it like some @scure imports do) in local modules,
          and import react-native-get-random-values first in those modules. */
      js: `require("react-native-get-random-values");`,
    },
    inject: ["./buffer.js"],
    outDir: "dist/native",
  },
  {
    ...commonConfig,
    entry: ["src/bare/index.ts"],
    format: ["cjs", "esm"],
    outDir: "dist/bare",
  },
]);
