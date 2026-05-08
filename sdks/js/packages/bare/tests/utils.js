const imports = {
  with: { imports: "bare-node-runtime/imports" },
};

let testQueue = Promise.resolve();
let queueScheduled = false;

function scheduleQueueExit() {
  if (queueScheduled) {
    return;
  }

  queueScheduled = true;

  // Defer until the current module finishes registering its tests.
  queueMicrotask(() => {
    testQueue
      .then(() => {
        process.exit(0);
      })
      .catch((err) => {
        console.error(
          "ERROR in test queue:",
          err && err.stack ? err.stack : err,
        );
        process.exit(1);
      });
  });
}

function test(name, fn) {
  scheduleQueueExit();

  testQueue = testQueue.then(async () => {
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
        } catch {
          console.error("  Expected:", String(expected));
          console.error("  Received:", String(received));
        }
        process.exit(1);
      }
    }

    try {
      await fn(assert);
      console.log(
        `PASS ${name} (${assertionCount} assertion${
          assertionCount === 1 ? "" : "s"
        })`,
      );
    } catch (err) {
      console.error(`ERROR in ${name}:`, err && err.stack ? err.stack : err);
      process.exit(1);
    }
  });
}

test.skip = function skip(name, _fn) {
  scheduleQueueExit();

  testQueue = testQueue.then(async () => {
    console.log(`SKIP ${name}`);
  });
};

async function retryUntilSuccess(
  fn,
  { maxAttempts = 20, delayMs = 2000 } = {},
) {
  let lastErr;
  for (let i = 1; i <= maxAttempts; i++) {
    try {
      return await fn();
    } catch (e) {
      lastErr = e;
      if (i < maxAttempts) {
        await new Promise((r) => setTimeout(r, delayMs));
      }
    }
  }
  throw lastErr;
}

module.exports = { test, imports, retryUntilSuccess };
