/* Polyfill NodeJS, approach from https://bit.ly/4hIsERg */

import { Buffer } from 'buffer';

if (typeof globalThis.Buffer === 'undefined') {
  globalThis.Buffer = Buffer;
}

if (typeof global === 'undefined') {
  window.global = window.globalThis;
}

export { Buffer };