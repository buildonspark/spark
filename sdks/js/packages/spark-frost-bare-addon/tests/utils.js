const process = require("bare-process");

function test(name, fn) {
  let assertionCount = 0;

  function assert(received, expected, message) {
    assertionCount += 1;
    const ok = received === expected;
    if (!ok) {
      const suffix = message ? `: ${message}` : "";
      console.error(`FAIL ${name} at assertion #${assertionCount}${suffix}`);
      try {
        const safeStringify = (v) =>
          JSON.stringify(v, (k, val) =>
            typeof val === "bigint" ? val.toString() : val,
          );
        console.error("  Expected:", safeStringify(expected));
        console.error("  Received:", safeStringify(received));
      } catch (_) {
        console.error("  Expected:", String(expected));
        console.error("  Received:", String(received));
      }
      process.exit(1);
    }
  }

  try {
    const maybePromise = fn(assert);
    if (maybePromise && typeof maybePromise.then === "function") {
      return maybePromise
        .then(() => {
          console.log(
            `PASS ${name} (${assertionCount} assertion${
              assertionCount === 1 ? "" : "s"
            })`,
          );
        })
        .catch((err) => {
          console.error(
            `ERROR in ${name}:`,
            err && err.stack ? err.stack : err,
          );
          process.exit(1);
        });
    }

    console.log(
      `PASS ${name} (${assertionCount} assertion${
        assertionCount === 1 ? "" : "s"
      })`,
    );
  } catch (err) {
    console.error(`ERROR in ${name}:`, err && err.stack ? err.stack : err);
    process.exit(1);
  }
}

const imports = {
  with: {
    imports: "./imports.json",
  },
};

module.exports = { test, imports };
