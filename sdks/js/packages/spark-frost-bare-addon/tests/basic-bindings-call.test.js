const binding = require("../index.js");
const { test } = require("./utils.js");

test("basic bindings call", (assert) => {
  assert(binding.hello(), "Hello from Rust");
});
