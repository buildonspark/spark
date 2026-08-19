import type { BrowserContext, Page } from "@playwright/test";
import { expect } from "@playwright/test";

export async function installWasmRecorder(
  context: BrowserContext,
): Promise<void> {
  await context.addInitScript(() => {
    const hashes: string[] = [];
    Object.defineProperty(window, "__sparkWasmSha256", { value: hashes });

    const originalInstantiate = WebAssembly.instantiate;
    Object.defineProperty(WebAssembly, "instantiate", {
      configurable: true,
      value: async (
        source: BufferSource | WebAssembly.Module,
        imports?: WebAssembly.Imports,
      ) => {
        let bytes: ArrayBuffer | undefined;
        if (source instanceof ArrayBuffer) {
          bytes = source.slice(0);
        } else if (ArrayBuffer.isView(source)) {
          const copy = new Uint8Array(source.byteLength);
          copy.set(
            new Uint8Array(source.buffer, source.byteOffset, source.byteLength),
          );
          bytes = copy.buffer;
        }

        if (bytes) {
          const digest = new Uint8Array(
            await crypto.subtle.digest("SHA-256", bytes),
          );
          hashes.push(
            Array.from(digest, (byte) =>
              byte.toString(16).padStart(2, "0"),
            ).join(""),
          );
        }

        return Reflect.apply(originalInstantiate, WebAssembly, [
          source,
          imports,
        ]);
      },
    });
  });
}

export async function instantiatedWasmHashes(page: Page): Promise<string[]> {
  return page.evaluate(
    () =>
      (
        window as Window & {
          __sparkWasmSha256?: string[];
        }
      ).__sparkWasmSha256 ?? [],
  );
}

export async function loadApp(page: Page): Promise<void> {
  await page.goto("/");
  await expect(
    page.getByRole("heading", { name: "Spark + Vite" }),
  ).toBeVisible();
}
