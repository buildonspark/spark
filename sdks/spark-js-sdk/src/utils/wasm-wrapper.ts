import init, { InitOutput } from "../wasm/spark_bindings.js";

export async function initWasm(): Promise<InitOutput> {
  let wasmModule: InitOutput;

  try {
    if (typeof window === "undefined") {
      // Node.js environment
      const path = require("path");
      const fs = require("fs").promises;
      const wasmPath = path.resolve(
        __dirname,
        "../wasm/wallet_bindings_bg.wasm"
      );

      const wasmBuffer = await fs.readFile(wasmPath);

      // Initialize with proper memory configuration for Node.js
      wasmModule = await init(wasmBuffer).catch((e) => {
        console.error("WASM initialization error:", e);
        throw e;
      });

      return wasmModule;
    } else {
      // Browser environment
      wasmModule = await init();
    }
  } catch (e) {
    console.error("WASM initialization error:", e);
    throw e;
  }

  // Verify the module is properly initialized
  if (!wasmModule || typeof wasmModule !== "object") {
    throw new Error("WASM module not properly initialized");
  }

  return wasmModule;
}
