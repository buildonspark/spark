export const isReactNative =
  typeof navigator !== "undefined" && navigator.product === "ReactNative";

export const isBun = globalThis.Bun !== undefined;
