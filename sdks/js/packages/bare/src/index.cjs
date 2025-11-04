"use strict";
module.exports = require("./entry.cjs", {
  with: { imports: "bare-node-runtime/imports" },
});
