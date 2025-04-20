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
