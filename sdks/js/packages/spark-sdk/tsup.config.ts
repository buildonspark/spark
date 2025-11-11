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
  esbuildOptions(options) {
    /* Turn import.meta warnings into errors for CJS builds to catch browser bindings imports */
    options.logOverride = {
      "empty-import-meta": "error",
    };
    // Ensure esbuild treats .wasm imports as file assets for bundlers
    options.loader = {
      ...options.loader,
      ".wasm": "binary",
    };
  },
};

export default defineConfig([
  {
    ...commonConfig,
    entry: [
      "src/index.node.ts",
      /* Entrypoints other than index should be static only, i.e. modules that never depend
         on the state of other modules. Everything else should be exported from index. */
      "src/tests/test-utils.ts",
      "src/proto/spark.ts",
      "src/proto/spark_token.ts",
      "src/graphql/objects/index.ts",
      "src/types/index.ts",
    ],
    inject: ["./buffer.js"],
    format: ["cjs", "esm"],
    outDir: "dist",
  },
  {
    ...commonConfig,
    entry: ["src/index.browser.ts"],
    inject: ["./buffer.js"],
    /* Only ESM format is supported for browser builds */
    format: ["esm"],
    outDir: "dist",
    onSuccess: "node scripts/on-build-success.js",
  },
  {
    ...commonConfig,
    entry: ["src/index.react-native.ts"],
    /* Lower target required for RN: */
    target: "es2020",
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
