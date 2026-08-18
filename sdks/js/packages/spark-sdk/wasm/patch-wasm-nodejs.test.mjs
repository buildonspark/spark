import assert from "node:assert/strict";
import test from "node:test";

import { patchNodejsModule } from "./patch-wasm-nodejs.mjs";

const wasmFooter = [
  "const wasmPath = `${__dirname}/wasm_nodejs_bg.wasm`;",
  "const wasmBytes = require('fs').readFileSync(wasmPath);",
  "const wasm = exports.__wasm = new WebAssembly.Instance(wasmModule, imports).exports;",
].join("\n");

test("patches named function and class exports from current wasm-bindgen", () => {
  const content = `
function calculate(value) {
  return value;
}
exports.calculate = calculate;
class Result {
  free() {}
}
exports.Result = Result;
function __wbg_get_imports() {
  return {};
}
const wasmBytes = require('fs').readFileSync('wasm_nodejs_bg.wasm');
const wasmModule = new WebAssembly.Module(wasmBytes);
let wasmInstance = new WebAssembly.Instance(wasmModule, __wbg_get_imports());
let wasm = wasmInstance.exports;`;

  const patched = patchNodejsModule(content, "AA==");

  assert.match(patched, /^const imports = \{\};$/m);
  assert.match(patched, /function calculate\(value\)/);
  assert.match(
    patched,
    /imports\.calculate = calculate;\nexport \{ calculate \};/,
  );
  assert.match(patched, /class ResultSrc \{/);
  assert.match(patched, /export const Result = imports\.Result = ResultSrc;/);
  assert.doesNotMatch(patched, /export const calculate/);
  assert.doesNotMatch(
    patched,
    /\bexports\.|\bmodule\.exports|\brequire\(|__dirname/,
  );
});

test("patches function-expression exports from older wasm-bindgen", () => {
  const content = `
let imports = {};
imports['__wbindgen_placeholder__'] = module.exports;
exports.calculate = function(value) {
  return value;
};
${wasmFooter}`;

  const patched = patchNodejsModule(content, "AA==");

  assert.match(
    patched,
    /export const calculate = imports\.calculate = function\(value\)/,
  );
  assert.doesNotMatch(
    patched,
    /\bexports\.|\bmodule\.exports|\brequire\(|__dirname/,
  );
});
