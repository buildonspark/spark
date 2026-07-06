import fs from "node:fs";
import { readFile, writeFile } from "node:fs/promises";

// Converts wasm-bindgen's nodejs-target CJS glue (wasm-bindgen 0.2.120
// format) to a self-contained ESM module: CJS exports become named ESM
// exports, and the .wasm file read is replaced with inlined base64 bytes so
// the module has no filesystem dependency.

const name = "wasm_token_primitives_nodejs";
const generatedDir = "./wasm/token-primitives/nodejs";
const outputDir = "./src/token-primitives-bindings/wasm";

const content = await readFile(`${generatedDir}/${name}.js`, "utf8");

const patched = `import { getCrypto } from "../../utils/crypto.js";

${content}`
  .replace(/\nexports\.(\w+) = \1;/g, "\nexport { $1 };")
  .replace("globalThis.crypto.getRandomValues(", "getCrypto().getRandomValues(")
  .replace(/\nconst\swasmPath\s=\s.*/g, "")
  .replace(
    /\nconst wasmBytes.*\n/,
    `
var __toBinary = /* @__PURE__ */ (() => {
  var table = new Uint8Array(128);
  for (var i = 0; i < 64; i++)
    table[i < 26 ? i + 65 : i < 52 ? i + 71 : i < 62 ? i - 4 : i * 4 - 205] = i;
  return (base64) => {
    var n = base64.length, bytes = new Uint8Array((n - (base64[n - 1] == "=") - (base64[n - 2] == "=")) * 3 / 4 | 0);
    for (var i2 = 0, j = 0; i2 < n; ) {
      var c0 = table[base64.charCodeAt(i2++)], c1 = table[base64.charCodeAt(i2++)];
      var c2 = table[base64.charCodeAt(i2++)], c3 = table[base64.charCodeAt(i2++)];
      bytes[j++] = c0 << 2 | c1 >> 4;
      bytes[j++] = c1 << 4 | c2 >> 2;
      bytes[j++] = c2 << 6 | c3;
    }
    return bytes;
  };
})();

const wasmBytes = __toBinary(${JSON.stringify(
      await readFile(`${generatedDir}/${name}_bg.wasm`, "base64"),
    )});
`,
  );

if (/\bexports\.|\brequire\(|__dirname/.test(patched)) {
  throw new Error(
    "patch-token-primitives-wasm-nodejs: CJS constructs survived patching — wasm-bindgen output format changed again; update this script",
  );
}

fs.mkdirSync(outputDir, { recursive: true });

await writeFile(
  `${outputDir}/wasm-nodejs.js`,
  patched,
);
fs.copyFileSync(
  `${generatedDir}/${name}.d.ts`,
  `${outputDir}/wasm-nodejs.d.ts`,
);
fs.copyFileSync(
  `${generatedDir}/${name}_bg.wasm`,
  `${outputDir}/wasm-nodejs-bg.wasm`,
);
fs.copyFileSync(
  `${generatedDir}/${name}_bg.wasm.d.ts`,
  `${outputDir}/wasm-nodejs-bg.wasm.d.ts`,
);
