import { readFile, writeFile } from "node:fs/promises";
import fs from "node:fs";

const name = "wasm_token_primitives_browser";
const generatedDir = "./wasm/token-primitives/browser";
const outputDir = "./src/token-primitives-bindings/wasm";

const content = await readFile(`${generatedDir}/${name}.js`, "utf8");

let patched = `import { getCrypto } from "../../utils/crypto.js";

${content}`.replace(
  "globalThis.crypto.getRandomValues(",
  "getCrypto().getRandomValues(",
);

// Drop the implicit import.meta.url fallback: the SDK always passes explicit
// wasm bytes, and a surviving relative-URL reference breaks consumers'
// bundlers (webpack tries to resolve it as a module asset).
patched = patched.replace(
  /if \((?:typeof module_or_path === 'undefined'|module_or_path === undefined)\)\s*\{\s*module_or_path = new URL\('[^']+\.wasm', import\.meta\.url\);\s*\}/,
  `if (module_or_path === undefined) {
        throw new Error('WASM module path must be provided. This should be set automatically by the SDK.');
    }`,
);

if (/new URL\('[^']*\.wasm'/.test(patched)) {
  throw new Error(
    "patch-token-primitives-wasm-browser: an implicit .wasm URL survived patching — wasm-bindgen output format changed again; update this script",
  );
}

fs.mkdirSync(outputDir, { recursive: true });

await writeFile(
  `${outputDir}/wasm-browser.js`,
  patched,
);

fs.copyFileSync(
  `${generatedDir}/${name}.d.ts`,
  `${outputDir}/wasm-browser.d.ts`,
);
fs.copyFileSync(
  `${generatedDir}/${name}_bg.wasm`,
  `${outputDir}/wasm-browser-bg.wasm`,
);
fs.copyFileSync(
  `${generatedDir}/${name}_bg.wasm.d.ts`,
  `${outputDir}/wasm-browser-bg.wasm.d.ts`,
);
