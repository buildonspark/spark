import { readFile, writeFile } from "node:fs/promises";
import fs from "node:fs";

const name = "wasm_token_primitives_browser";
const generatedDir = "./wasm/token-primitives/browser";
const outputDir = "./src/token-primitives-bindings/wasm";

const content = await readFile(`${generatedDir}/${name}.js`, "utf8");

let patched = content.replace(
  `${name}_bg.wasm`,
  "./token-primitives-bindings/wasm/wasm-browser-bg.wasm",
);

patched = patched.replace(
  /if \(typeof module_or_path === 'undefined'\)\s*\{\s*module_or_path = new URL\('[^']+\.wasm', import\.meta\.url\);\s*\}/,
  `if (typeof module_or_path === 'undefined') {
        throw new Error('WASM module path must be provided. This should be set automatically by the SDK.');
    }`,
);

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
