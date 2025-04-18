/* Convert nodejs wasm module output to ESM and inline wasm so that it can be
   used in both browser and nodejs. See https://bit.ly/4iGErRo. */

import { readFile, writeFile } from "node:fs/promises";

const name = "spark_bindings_nodejs";
const generatedDir = "./wasm/nodejs";

const content = await readFile(`${generatedDir}/${name}.js`, "utf8");

const patched = content
  // use global TextDecoder TextEncoder
  .replace("require(`util`)", "globalThis")
  // handle class exports (https://bit.ly/421kbmk):
  .replace(/\nclass (.*?) \{/g, "\nclass $1Src {")
  .replace(
    /\nmodule\.exports\.(.*?) = \1;/g,
    "\nexport const $1 = imports.$1 = $1Src ",
  )
  // attach to `imports` instead of module.exports
  .replace("= module.exports", "= imports")
  .replace(/\nmodule\.exports\.(.*?)\s+/g, "\nexport const $1 = imports.$1 ")
  .replace(/$/, "export default imports")
  // inline bytes Uint8Array
  .replace(
    /\nconst path.*\nconst bytes.*\n/,
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

const bytes = __toBinary(${JSON.stringify(
      await readFile(`${generatedDir}/${name}_bg.wasm`, "base64"),
    )});
`,
  );

await writeFile(`./src/wasm/spark_bindings.js`, patched);
await writeFile(
  `./src/wasm/spark_bindings.d.ts`,
  await readFile(`${generatedDir}/${name}.d.ts`, "utf8"),
);
