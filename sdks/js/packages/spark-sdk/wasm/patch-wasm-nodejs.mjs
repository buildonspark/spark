/* Convert nodejs wasm module output to ESM and inline wasm so that it can be
   used in both browser and nodejs. See https://bit.ly/4iGErRo. */

import fs from "node:fs";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { pathToFileURL } from "node:url";

const name = "wasm_nodejs";
const generatedDir = "./wasm/nodejs";

export function patchNodejsModule(content, wasmBase64) {
  const declaredFunctions = new Set(
    [...content.matchAll(/^function ([A-Za-z_$][\w$]*)\s*\(/gm)].map(
      ([, functionName]) => functionName,
    ),
  );
  const declaredClasses = new Set(
    [...content.matchAll(/^class ([A-Za-z_$][\w$]*)\s*\{/gm)].map(
      ([, className]) => className,
    ),
  );
  const importsDeclaration = /^(?:const|let|var)\s+imports\b/m.test(content)
    ? ""
    : "const imports = {};\n";

  const patched = `import { getCrypto } from "../../utils/crypto.js";

${importsDeclaration}${content}`
    // use global TextDecoder TextEncoder
    .replace("require(`util`)", "globalThis")
    // handle class exports (https://bit.ly/421kbmk):
    .replace(/^class ([A-Za-z_$][\w$]*)\s*\{/gm, "class $1Src {")
    .replace(/\b(?!Uint8Array)(\w+)\.prototype/g, "$1Src.prototype")
    .replace(/^exports\.([A-Za-z_$][\w$]*) = \1;$/gm, (_, exportName) => {
      if (declaredFunctions.has(exportName)) {
        return `imports.${exportName} = ${exportName};\nexport { ${exportName} };`;
      }
      if (declaredClasses.has(exportName)) {
        return `export const ${exportName} = imports.${exportName} = ${exportName}Src;`;
      }
      throw new Error(
        `patch-wasm-nodejs: unsupported named export assignment for ${exportName}`,
      );
    })
    // attach to `imports` instead of exports
    .replace("= exports", "= imports")
    .replace("= module.exports", "= imports")
    .replace(/\nexports\.(.*?)\s+/g, "\nexport const $1 = imports.$1 ")
    .replace(/$/, "export default imports")
    .replace(/\nconst\swasmPath\s=\s.*/g, "")
    .replace("const ret = arg0.crypto;", "const ret = getCrypto();")
    .replace(
      /const ret = module\.require;\s*return ret;/,
      `throw new Error(
        "WASM ESM wrapper should receive crypto via setCrypto(), not module.require."
    );`,
    )
    // inline bytes Uint8Array
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

const wasmBytes = __toBinary(${JSON.stringify(wasmBase64)});
`,
    );

  if (/\bexports\.|\bmodule\.exports|\brequire\(|__dirname/.test(patched)) {
    throw new Error(
      "patch-wasm-nodejs: CJS constructs survived patching; wasm-bindgen output format changed again",
    );
  }
  return patched;
}

async function patchGeneratedFiles() {
  const content = await readFile(`${generatedDir}/${name}.js`, "utf8");
  const wasmBase64 = await readFile(
    `${generatedDir}/${name}_bg.wasm`,
    "base64",
  );
  const patched = patchNodejsModule(content, wasmBase64);

  await writeFile(`./src/spark-bindings/wasm/wasm-nodejs.js`, patched);
  fs.copyFileSync(
    `${generatedDir}/${name}.d.ts`,
    `./src/spark-bindings/wasm/wasm-nodejs.d.ts`,
  );
  fs.copyFileSync(
    `${generatedDir}/${name}_bg.wasm`,
    `./src/spark-bindings/wasm/wasm-nodejs-bg.wasm`,
  );
  fs.copyFileSync(
    `${generatedDir}/${name}_bg.wasm.d.ts`,
    `./src/spark-bindings/wasm/wasm-nodejs-bg.wasm.d.ts`,
  );
}

const invokedPath =
  process.argv[1] && pathToFileURL(path.resolve(process.argv[1])).href;
if (invokedPath === import.meta.url) {
  await patchGeneratedFiles();
}
