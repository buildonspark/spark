import { defineConfig } from "tsup";

export default defineConfig({
  entry: [
    "src/index.ts",
    "src/types.ts",
    "src/lrc/api/grpc/*.ts",
    "src/lrc/types/index.ts",
    "src/lrc/utils/index.ts",
    "src/lrc/wallet/index.ts",
    "src/proto/rpc/v1/types.ts",
  ],
  format: ["cjs", "esm"],
  inject: ['./buffer.js'],
  dts: true,
  clean: true,
});
