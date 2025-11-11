import * as spark from "@buildonspark/spark-sdk";
import { SparkWallet } from "@buildonspark/spark-sdk";

interface SparkGlobalThis {
  s: typeof spark;
}

declare const globalThis: SparkGlobalThis;

globalThis.s = spark;

let wallet: SparkWallet | null = null;
SparkWallet.initialize({}).then(({ wallet: initializedWallet }) => {
  console.log(
    "[spark-extension] SparkWallet initialised in background",
    initializedWallet,
  );
  wallet = initializedWallet;
});

console.log("[spark-extension] SparkWallet initialized in background", wallet);

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message?.type === "PING_FROM_CONTENT") {
    console.log(
      "[spark-extension] background received PING_FROM_CONTENT",
      message,
    );
    sendResponse({
      ok: true,
      from: "background",
      walletState: wallet ? "ready" : "uninitialized",
      randomNumber: Math.random(),
    });
    return true;
  }
  return false;
});
