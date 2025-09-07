/* Import node libs to polyfill browser objects */
import crypto from "crypto";
import { TextDecoder, TextEncoder } from "util";
import nodeFetch, {
  RequestInit as NodeFetchRequestInit,
  Response,
} from "node-fetch";
import fs from "fs";
import { fileURLToPath } from "url";

const customFetch = async (url: string | URL, init?: NodeFetchRequestInit) => {
  if (url.toString().startsWith("file://")) {
    try {
      const filePath = fileURLToPath(url);
      const buffer = fs.readFileSync(filePath);
      return new Response(buffer);
    } catch (error) {
      throw error;
    }
  }
  return nodeFetch(url, init);
};

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
    value: customFetch,
  },
});
