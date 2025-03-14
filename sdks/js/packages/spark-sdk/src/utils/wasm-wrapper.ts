import init, { InitOutput } from "../wasm/spark_bindings.js";

export async function initWasm(): Promise<InitOutput> {
  let wasmModule: InitOutput;

  try {
    if (typeof window === "undefined") {
      // Node.js environment
      try {
        // Dynamic imports for Node.js modules to avoid browser compatibility issues
        const fs = await import("fs/promises");
        const path = await import("path");
        const url = await import("url");

        const __filename = url.fileURLToPath(import.meta.url);
        const __dirname = path.dirname(__filename);

        const wasmPath = path.resolve(
          __dirname,
          "../wasm/spark_bindings_bg.wasm",
        );

        const wasmBuffer = await fs.readFile(wasmPath);

        // Initialize with proper memory configuration for Node.js
        wasmModule = await init({ module_or_path: wasmBuffer }).catch((e) => {
          console.error("WASM initialization error:", e);
          throw e;
        });

        return wasmModule;
      } catch (e) {
        console.error(
          "Error with Node.js-specific WASM loading, falling back to standard initialization:",
          e,
        );
        // Fall back to standard initialization if dynamic imports fail
        wasmModule = await init();
        return wasmModule;
      }
    } else {
      // Browser environment
      const response = await fetch(
        new URL("../wasm/spark_bindings_bg.wasm", import.meta.url),
      );
      if (!response.ok) {
        throw new Error(`Failed to load WASM file: ${response.statusText}`);
      }
      const wasmBuffer = await response.arrayBuffer();
      wasmModule = await init({ module_or_path: wasmBuffer });
      return wasmModule;
    }
  } catch (e) {
    console.error("WASM initialization error:", e);
    throw e;
  }
}
