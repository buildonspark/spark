import { SparkWallet } from "@buildonspark/bare";
import { globalAgent as http1Agent } from "bare-http1";
import { globalAgent as httpsAgent } from "bare-https";
import process from "bare-process";
import walletConfig, { getExampleWalletOptions } from "./wallet-config.js";

const WATCHDOG_MS = Number(process.env.WATCHDOG_MS ?? 30_000);
const CLEANUP_CONNECTIONS = process.env.CLEANUP_CONNECTIONS !== "0";

function log(message, details) {
  const prefix = `[${new Date().toISOString()}]`;
  if (details === undefined) {
    console.log(`${prefix} ${message}`);
    return;
  }
  console.log(`${prefix} ${message}`, details);
}

function formatError(error) {
  if (error instanceof Error) {
    return `${error.name}: ${error.message}`;
  }
  return String(error);
}

async function raceWithWatchdog(label, promise) {
  const start = Date.now();
  let watchdogTimer;
  const watchdog = new Promise((resolve) => {
    watchdogTimer = setTimeout(
      () => resolve({ type: "watchdog" }),
      WATCHDOG_MS,
    );
  });
  const result = await Promise.race([
    promise.then(
      (value) => ({ type: "resolved", value }),
      (error) => ({ type: "rejected", error }),
    ),
    watchdog,
  ]).finally(() => {
    clearTimeout(watchdogTimer);
  });
  const elapsedMs = Date.now() - start;

  if (result.type === "resolved") {
    log(`${label} resolved after ${elapsedMs}ms`, result.value);
  } else if (result.type === "rejected") {
    log(`${label} rejected after ${elapsedMs}ms`, formatError(result.error));
  } else {
    log(`${label} still pending after ${elapsedMs}ms`);
  }

  return result;
}

function suspendAgents() {
  http1Agent.suspend();
  httpsAgent.suspend();
  log("agents suspended");
}

function resumeAgents() {
  http1Agent.resume();
  httpsAgent.resume();
  log("agents resumed");
}

async function nextTick() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

async function main() {
  log("starting Bare agent suspend balance repro", {
    cleanupConnections: CLEANUP_CONNECTIONS,
    network: process.env.NETWORK ?? process.env.SPARK_NETWORK ?? "MAINNET",
    watchdogMs: WATCHDOG_MS,
  });

  const options = getExampleWalletOptions(process.env, "MAINNET");
  const { wallet } = await SparkWallet.initialize({
    mnemonicOrSeed: process.env.MNEMONIC ?? walletConfig.mnemonic,
    options,
  });

  log("wallet initialized");
  await raceWithWatchdog("initial balance", wallet.getBalance());

  suspendAgents();
  const alreadySuspendedBalance = wallet.getBalance();
  await raceWithWatchdog(
    "balance while agents already suspended",
    alreadySuspendedBalance,
  );
  resumeAgents();
  await raceWithWatchdog(
    "same already-suspended balance promise after resume",
    alreadySuspendedBalance,
  );

  const inFlightBalance = wallet.getBalance();
  await nextTick();
  suspendAgents();
  await raceWithWatchdog("balance suspended while in flight", inFlightBalance);
  resumeAgents();

  await raceWithWatchdog("fresh balance after resume", wallet.getBalance());

  if (CLEANUP_CONNECTIONS) {
    log("cleaning up wallet connections");
    await wallet.cleanupConnections();
  } else {
    log(
      "skipping wallet cleanup; process should exit only if no SDK handles keep it alive",
    );
  }

  log("repro script completed");
}

main().catch((error) => {
  log("repro script failed", formatError(error));
  process.exit(1);
});
