import * as spark from "@buildonspark/spark-sdk";
import { SparkWallet } from "@buildonspark/spark-sdk";

let wallet: SparkWallet | null = null;

function getMessageType(message: unknown): string | undefined {
  if (typeof message !== "object" || message === null || !("type" in message)) {
    return undefined;
  }

  const { type } = message as { type?: unknown };
  return typeof type === "string" ? type : undefined;
}

void SparkWallet.initialize({
  options: {
    network: "REGTEST",
  },
})
  .then(({ wallet: initializedWallet }) => {
    console.log(
      "[spark-extension] SparkWallet initialised in background",
      initializedWallet,
    );
    wallet = initializedWallet;
  })
  .catch((error: unknown) => {
    console.error("Failed to initialize SparkWallet", error);
  });

console.log("[spark-extension] SparkWallet initialized in background", wallet);

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  const type = getMessageType(message);

  if (type === "GET_WALLET_ADDRESS") {
    console.log(
      "[spark-extension] background received GET_WALLET_ADDRESS",
      message,
    );

    if (!wallet) {
      sendResponse({
        ok: false,
        walletState: "uninitialized",
        error: "Wallet not yet initialized",
      });
      return true;
    }

    // Get wallet address asynchronously
    void (async () => {
      try {
        const address = await wallet.getSparkAddress();
        sendResponse({
          ok: true,
          walletState: "ready",
          address: address,
        });
      } catch (error) {
        sendResponse({
          ok: false,
          walletState: "error",
          error: error instanceof Error ? error.message : String(error),
        });
      }
    })();

    return true; // Will respond asynchronously
  }

  if (type === "PING_FROM_CONTENT") {
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

interface SparkGlobalThis {
  s: typeof spark;
}

declare const globalThis: SparkGlobalThis;

/* For debugging purposes only, not required: */
globalThis.s = spark;
