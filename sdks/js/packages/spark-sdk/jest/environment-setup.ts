/* Import node libs to polyfill browser objects */
import crypto from "crypto";
import { TextDecoder, TextEncoder } from "util";
import fetch from "node-fetch";

Object.defineProperties(globalThis, {
  crypto: {
    value: {
      getRandomValues: (arr: NodeJS.ArrayBufferView) =>
        crypto.randomFillSync(arr),
      subtle: crypto.webcrypto.subtle,
    },
  },
  TextEncoder: {
    value: TextEncoder,
  },
  TextDecoder: {
    value: TextDecoder,
  },
  fetch: {
    value: fetch,
  },
});

/* Initialize SparkFrost WASM bindings for tests */
import { setSparkFrostOnce } from "../src/spark-bindings/spark-bindings.js";
import { SparkFrost } from "../src/spark-bindings/spark-bindings.node.js";

setSparkFrostOnce(new SparkFrost());
