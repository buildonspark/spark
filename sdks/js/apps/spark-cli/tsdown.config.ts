import { readFileSync } from "node:fs";
import { defineConfig } from "tsdown";

const { version } = JSON.parse(
  readFileSync(new URL("./package.json", import.meta.url), "utf8"),
);

export default defineConfig({
  entry: ["src/cli.ts"],
  format: ["esm"],
  outDir: "dist",
  sourcemap: false,
  dts: false,
  clean: true,
  // Externalize all npm deps — spark-sdk has WASM binaries that can't be bundled
  external: [/^[^./]/],
  env: {
    SPARK_CLI_VERSION: version,
  },
  outputOptions: {
    banner: "#!/usr/bin/env node",
  },
});
