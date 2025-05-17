import nodeCrypto from "crypto";

export const getCrypto = (): Crypto => {
  let cryptoImpl: any =
    typeof window !== "undefined"
      ? window.crypto
      : typeof global !== "undefined" && global.crypto
        ? global.crypto
        : nodeCrypto;

  return cryptoImpl as Crypto;
};
