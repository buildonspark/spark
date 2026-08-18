/* Patch wasm-bindgen's browser output for SDK-controlled WASM initialization. */

import { readFile, writeFile } from "node:fs/promises";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const name = "wasm_browser";
const generatedDir = "./wasm/browser";

export function removeImplicitWasmUrl(content) {
  const patched = content.replace(
    /if \((?:typeof module_or_path === 'undefined'|module_or_path === undefined)\)\s*\{\s*module_or_path = new URL\('[^']+\.wasm', import\.meta\.url\);\s*\}/,
    `if (module_or_path === undefined) {
        throw new Error('WASM module path must be provided. This should be set automatically by the SDK.');
    }`,
  );

  if (/new URL\(['"][^'"]*\.wasm['"]/.test(patched)) {
    throw new Error(
      "patch-wasm-browser: an implicit .wasm URL survived patching; wasm-bindgen output format changed again",
    );
  }
  return patched;
}

async function patchGeneratedFiles() {
  const content = await readFile(`${generatedDir}/${name}.js`, "utf8");
  let patched = content.replace(
    "wasm_browser_bg.wasm",
    "./spark-bindings/wasm/wasm-browser-bg.wasm",
  );

  patched = `import { getCrypto } from "../../utils/crypto.js";

${patched}`
    .replace("const ret = arg0.crypto;", "const ret = getCrypto();")
    .replace(
      /const ret = module\.require;\s*return ret;/,
      `throw new Error(
            "WASM ESM wrapper should receive crypto via setCrypto(), not module.require."
        );`,
    );

  // The SDK always supplies explicit bytes. A surviving fallback makes
  // bundlers resolve a relative WASM asset that the package does not contain.
  patched = removeImplicitWasmUrl(patched);

  await writeFile(`./src/spark-bindings/wasm/wasm-browser.js`, patched);

  fs.copyFileSync(
    `${generatedDir}/${name}.d.ts`,
    `./src/spark-bindings/wasm/wasm-browser.d.ts`,
  );
  fs.copyFileSync(
    `${generatedDir}/${name}_bg.wasm`,
    `./src/spark-bindings/wasm/wasm-browser-bg.wasm`,
  );
  fs.copyFileSync(
    `${generatedDir}/${name}_bg.wasm.d.ts`,
    `./src/spark-bindings/wasm/wasm-browser-bg.wasm.d.ts`,
  );
}

const invokedPath =
  process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href;
if (invokedPath === import.meta.url) {
  await patchGeneratedFiles();
}
