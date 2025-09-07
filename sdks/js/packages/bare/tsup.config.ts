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
    entry: ["src/index.ts"],
    format: ["cjs", "esm"],
    outDir: "dist",
  },
]);
