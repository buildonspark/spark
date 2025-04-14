import nodeCrypto from "crypto";

export const getCrypto = (): Crypto => {
  let cryptoImpl: any =
    typeof window !== "undefined"
      ? window.crypto
      : typeof global !== "undefined" && global.crypto
        ? global.crypto
        : nodeCrypto;

  // Add randomUUID if it doesn't exist
  if (!cryptoImpl?.randomUUID && nodeCrypto.randomUUID) {
    cryptoImpl = {
      ...cryptoImpl,
      randomUUID: nodeCrypto.randomUUID,
    };
  }

  return cryptoImpl as Crypto;
};
