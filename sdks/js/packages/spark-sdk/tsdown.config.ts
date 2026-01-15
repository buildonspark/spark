import { readFileSync } from "node:fs";
import { defineConfig } from "tsdown";

const pkg = JSON.parse(
  readFileSync(new URL("./package.json", import.meta.url), "utf8"),
);

const commonConfig = {
  sourcemap: false,
  dts: true,
  clean: false,
  fixedExtension: false,
  define: {
    __PACKAGE_VERSION__: JSON.stringify(pkg.version),
  },
  inputOptions: {
    moduleTypes: {
      ".wasm": "binary",
    },
  },
};

const rootEntryConfig = {
  ...commonConfig,
  entry: [
    "src/index.node.ts",
    /* Entrypoints other than index should be static only, i.e. modules that never depend
       on the state of other modules. Everything else should be exported from index. */
    "src/proto/spark.ts",
    "src/proto/spark_token.ts",
    "src/types/index.ts",
  ],
  outDir: "dist",
};

export default defineConfig([
  { ...rootEntryConfig, format: ["cjs"] },
  {
    ...commonConfig,
    entry: [
      ...rootEntryConfig.entry,
      "src/index.browser.ts",
      /* Entrypoints other than index should be static only, i.e. modules that never depend
         on the state of other modules. Everything else should be exported from index. */
      "src/tests/test-utils.ts",
    ],
    format: ["esm"],
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
    outDir: "dist/native",
  },
  {
    ...commonConfig,
    entry: ["src/bare/index.ts"],
    format: ["cjs", "esm"],
    outDir: "dist/bare",
  },
]);
