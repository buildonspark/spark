import { Buffer } from "buffer";
globalThis.Buffer = Buffer;

if (typeof global === "undefined") {
  (window as any).global = window;
}
