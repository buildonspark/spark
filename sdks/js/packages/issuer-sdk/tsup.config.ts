import { defineConfig } from "tsup";

export default defineConfig({
  entry: [
    "src/index.ts",
    "src/types.ts"
  ],
  format: ["cjs", "esm"],
  dts: true,
  clean: true,
  inject: ['./buffer.js'],
});
