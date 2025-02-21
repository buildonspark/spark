import crypto from "crypto";

export const getCrypto = (): Crypto => {
  // Browser environment
  if (typeof window !== "undefined" && window.crypto) {
    return window.crypto;
  }
  // Node.js environment
  if (typeof global !== "undefined" && global.crypto) {
    return global.crypto;
  }
  // Node.js environment without global.crypto (older versions)
  return crypto as Crypto;
};
