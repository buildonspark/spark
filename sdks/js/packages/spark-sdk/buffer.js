/* Polyfill NodeJS, approach from https://bit.ly/4hIsERg */

import { Buffer } from "buffer";

if (typeof globalThis.Buffer === "undefined") {
  globalThis.Buffer = Buffer;
}

if (typeof window !== "undefined") {
  if (typeof window.global === "undefined") {
    window.global = window;
  }
  if (typeof window.globalThis === "undefined") {
    window.globalThis = window;
  }
}

export { Buffer };
