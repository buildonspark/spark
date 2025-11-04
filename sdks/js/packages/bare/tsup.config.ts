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
    entry: ["src/entry.ts"],
    format: ["cjs", "esm"],
    outDir: "dist",
    onSuccess:
      "cp ./src/index.js ./dist/index.js && cp ./src/index.cjs ./dist/index.cjs && cp ./src/index.d.ts ./dist/index.d.ts && cp ./src/index.d.cts ./dist/index.d.cts",
  },
]);
