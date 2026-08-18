import assert from "node:assert/strict";
import test from "node:test";

import { removeImplicitWasmUrl } from "./patch-wasm-browser.mjs";

for (const condition of [
  "typeof module_or_path === 'undefined'",
  "module_or_path === undefined",
]) {
  test(`removes the ${condition} WASM URL fallback`, () => {
    const content = `
if (${condition}) {
  module_or_path = new URL('wasm_browser_bg.wasm', import.meta.url);
}`;

    const patched = removeImplicitWasmUrl(content);

    assert.match(patched, /WASM module path must be provided/);
    assert.doesNotMatch(patched, /new URL\(/);
  });
}

test("rejects an unrecognized implicit WASM URL fallback", () => {
  assert.throws(
    () =>
      removeImplicitWasmUrl(
        "module_or_path ??= new URL('wasm_browser_bg.wasm', import.meta.url);",
      ),
    /implicit \.wasm URL survived patching/,
  );
});
