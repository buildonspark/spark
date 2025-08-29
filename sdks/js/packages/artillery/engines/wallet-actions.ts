import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { IssuerSparkWalletNoEvents } from "./issuer-wallet-no-events";
import { getLoadtestNetworkConfig } from "./network-config";

type WalletType = IssuerSparkWallet | IssuerSparkWalletNoEvents;

import { WalletPoolManager } from "./wallet-pool-manager";
import {
  beforeScenario as lockWalletsHook,
  afterScenario as unlockWalletsHook,
  lockedWallets,
  createLockFile as createLockFileFromHooks,
  removeLockFile as removeLockFileFromHooks,
  isLocked as isLockedFromHooks,
} from "./hooks";
import type { SparkContext, ArtilleryEventEmitter, EngineStep, WalletParams } from "./types";
import { randomUUID } from "crypto";
import { walletPools } from "./hooks";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import * as fs from "fs";
import * as path from "path";

const createLockFile = createLockFileFromHooks;
const removeLockFile = removeLockFileFromHooks;
const isLocked = isLockedFromHooks;

async function safeGetBalance(wallet: IssuerSparkWallet): Promise<{ balance: bigint }> {
  let retries = 8;
  let lastError: any = null;

  while (retries > 0) {
    try {
      return await wallet.getBalance();
    } catch (error: any) {
      lastError = error;

      if (
        error.message?.includes("EADDRNOTAVAIL") ||
        error.message?.includes("No connection established") ||
        error.message?.includes("Authentication connection error")
      ) {
        console.warn(`Connection error during balance check (${retries} retries left): ${error.message}`);
        if (retries > 1) {
          await new Promise((resolve) => setTimeout(resolve, 2000));
          retries--;
          continue;
        }
      }

      if (
        error.message?.includes("Failed to claim transfer") ||
        error.message?.includes("claimTransfer") ||
        error.context?.operation === "claimTransfer"
      ) {
        console.warn(`Warning: Failed to claim transfer during balance check: ${error.message}`);
        await new Promise((resolve) => setTimeout(resolve, 1000));
        return await wallet.getBalance();
      }

      throw error;
    }
  }

  throw lastError || new Error("Failed to get balance after retries");
}

async function initializeWalletWithRetry<T extends WalletType>(
  initFunction: () => Promise<{ wallet: T; mnemonic?: string }>,
  maxRetries: number = 8,
  retryDelay: number = 5000
): Promise<{ wallet: T; mnemonic?: string }> {
  let lastError: Error | null = null;

  for (let attempt = 1; attempt <= maxRetries; attempt++) {
    try {
      console.log(`  Wallet initialization attempt ${attempt}/${maxRetries}...`);
      const result = await initFunction();
      return result;
    } catch (error: any) {
      lastError = error;

      if (
        error.message?.includes("EADDRNOTAVAIL") ||
        error.message?.includes("No connection established") ||
        error.message?.includes("Authentication connection error")
      ) {
        console.warn(`  Connection error on attempt ${attempt}/${maxRetries}: ${error.message}`);

        if (attempt < maxRetries) {
          console.log(`  Will retry with fresh connection in 2 seconds...`);
          await new Promise((resolve) => setTimeout(resolve, 2000));
        }
      } else if (
        error.message?.includes("verify_challenge timed out") ||
        error.message?.includes("Authentication failed") ||
        error.context?.reason?.includes("verify_challenge timed out")
      ) {
        console.warn(`  Authentication timeout on attempt ${attempt}/${maxRetries}: ${error.message}`);

        if (attempt < maxRetries) {
          console.log(`  Waiting ${retryDelay}ms before retry...`);
          await new Promise((resolve) => setTimeout(resolve, retryDelay));

          retryDelay = Math.min(retryDelay * 2, 30000);
        }
      } else {
        throw error;
      }
    }
  }

  throw new Error(`Failed to initialize wallet after ${maxRetries} attempts. Last error: ${lastError?.message}`);
}

export class WalletActions {
  private poolManager: WalletPoolManager;
  private engine: any;

  constructor(
    private ee: ArtilleryEventEmitter,
    engine?: any
  ) {
    this.poolManager = WalletPoolManager.getInstance();
    this.engine = engine;
  }

  initNamedWallet(params: { name: string; mnemonic?: string } = { name: "" }): EngineStep {
    const ee = this.ee;
    const poolManager = this.poolManager;

    return async function (context: SparkContext, callback) {
      const startTime = Date.now();

      try {
        if (!params.name) {
          throw new Error("Wallet name is required");
        }

        if (poolManager.getNamedWallet(params.name)) {
          console.log(`Wallet ${params.name} already exists`);
          callback(null, context);
          return;
        }

        const mnemonic = params.mnemonic || generateMnemonic(wordlist, 256);
        console.log(`Initializing named wallet: ${params.name}`);

        const walletInitStartTime = Date.now();
        const { wallet } = await initializeWalletWithRetry(() =>
          IssuerSparkWallet.initialize({
            mnemonicOrSeed: mnemonic,
            options: {
              network: (process.env.SPARK_NETWORK || "LOCAL") as "REGTEST" | "MAINNET" | "TESTNET" | "SIGNET" | "LOCAL",
              threshold: 3,
              tokenTransactionVersion: "V1" as const,
              tokenSignatures: "SCHNORR" as const,
            },
          })
        );

        const walletInitEndTime = Date.now();
        console.log(`Named wallet ${params.name} initialization took ${walletInitEndTime - walletInitStartTime}ms`);

        await poolManager.registerNamedWallet(params.name, wallet, mnemonic);

        const walletInfo = poolManager.getNamedWallet(params.name)!;
        console.log(
          `Named wallet ${params.name} initialized: ${walletInfo.address.substring(0, 10)}... in ${walletInitEndTime - walletInitStartTime}ms`
        );

        ee.emit("histogram", "spark.named_wallet_init_time", Date.now() - startTime);
        ee.emit("counter", "spark.named_wallet_initialized", 1);

        callback(null, context);
      } catch (error) {
        console.error(`Named wallet initialization failed for ${params.name}:`, error.message);
        ee.emit("counter", "spark.named_wallet_init_failed", 1);
        callback(error);
      }
    };
  }

  fundWallet(params: { walletName?: string; amount: number }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      try {
        const walletInfo = params.walletName ? context.vars?.[params.walletName] : context.sparkWallet;
        if (!walletInfo) {
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        console.log(`Funding ${walletInfo.name} wallet with ${params.amount} sats...`);

        console.log(`Please fund wallet ${walletInfo.name} with ${params.amount} sats using external means`);

        const balance = await safeGetBalance(walletInfo.wallet);
        console.log(`${walletInfo.wallet} balance: ${balance.balance} sats`);

        if (params.walletName) {
          if (context.vars?.[params.walletName]) {
            context.vars[params.walletName].balance = balance.balance;
          }
        }

        ee.emit("counter", "spark.wallet_funded", 1);
        callback(null, context);
      } catch (error) {
        console.error("Wallet funding failed:", error.message);
        ee.emit("counter", "spark.wallet_funding_failed", 1);
        callback(error);
      }
    };
  }

  lockWallets(params?: any): EngineStep {
    return async function (context: SparkContext, callback) {
      try {
        const vuId = (context as any)._uid || context.vars?.$uuid || randomUUID();

        const modifiedParams = { ...params };
        if (modifiedParams?.wallets && Array.isArray(modifiedParams.wallets)) {
          modifiedParams.wallets = modifiedParams.wallets.map((wallet: any) => {
            if (typeof wallet === "object" && wallet.name) {
              return {
                ...wallet,
                name: `${wallet.name}_${vuId}`,
                originalName: wallet.name,
              };
            }
            return wallet;
          });
        }

        context.vars = context.vars || {};
        context.vars._vuId = vuId;
        context.vars._walletNameMap = {};

        if (modifiedParams?.wallets) {
          modifiedParams.wallets.forEach((wallet: any) => {
            if (wallet.originalName) {
              context.vars._walletNameMap[wallet.originalName] = wallet.name;
            }
          });
        }

        context._scenarioSpec = context._scenarioSpec || {};
        context._scenarioSpec.lockWallets = modifiedParams || context._scenarioSpec?.lockWallets;

        await new Promise((resolve, reject) => {
          lockWalletsHook(context, {} as ArtilleryEventEmitter, (error: any) => {
            if (error) reject(error);
            else resolve(undefined);
          });
        });

        callback(null, context);
      } catch (error) {
        callback(error);
      }
    };
  }

  unlockWallets(params?: { wallets?: string[] }): EngineStep {
    const poolManager = this.poolManager;

    return async function (context: SparkContext, callback) {
      try {
        console.log("UnlockWallets: Unlocking wallets and closing connections...");

        const walletsToUnlock = params?.wallets || context.scenarioLockedWallets || [];

        for (const walletName of walletsToUnlock) {
          const namedWallet = context.vars?.[walletName];
          const lockedInfo = lockedWallets.get(walletName);

          if (lockedInfo) {
            const address = await lockedInfo.wallet.getSparkAddress();

            if (await removeLockFile(address)) {
              console.log(`  Removed lock file for wallet "${walletName}" (${address.substring(0, 10)}...)`);
            }

            const pool = walletPools.get(lockedInfo.pool);
            if (pool) {
              pool.available.push(lockedInfo.wallet);
              console.log(`  Returned wallet "${walletName}" to pool "${lockedInfo.pool}"`);

              try {
                await poolManager.unlockWallet(lockedInfo.wallet);
              } catch (error) {
                console.log(`  Note: Wallet state not saved (not managed by pool manager)`);
              }
            }

            lockedWallets.delete(walletName);
          }

          if (context.vars?.[walletName]) {
            delete context.vars[walletName];
          }
        }

        if (context.scenarioLockedWallets) {
          context.scenarioLockedWallets = context.scenarioLockedWallets.filter(
            (name) => !walletsToUnlock.includes(name)
          );
        }

        console.log(`UnlockWallets: Unlocked ${walletsToUnlock.length} wallets`);
        callback(null, context);
      } catch (error) {
        console.error("UnlockWallets failed:", error);
        callback(error);
      }
    };
  }

  getBalance(params?: { walletName?: string; storeAs?: string }): EngineStep {
    const ee = this.ee;
    return async function (context: SparkContext, callback) {
      try {
        const walletInfo = params?.walletName ? context.vars?.[params.walletName] : context.sparkWallet;

        if (!walletInfo || !walletInfo.wallet) {
          throw new Error(`Wallet ${params?.walletName || "default"} not found. Initialize it first with initWallet`);
        }

        const { balance } = await safeGetBalance(walletInfo.wallet);

        if (params?.storeAs) {
          context.vars = context.vars || {};
          context.vars[params.storeAs] = balance;
        }

        console.log(
          `${walletInfo.name || params?.walletName || "Wallet"}, 
          ((${walletInfo.address})) (${await walletInfo.wallet.getTokenL1Address()}) 
          balance: ${balance} sats${params?.storeAs ? ` (stored as ${params.storeAs})` : ""}`
        );

        ee.emit("counter", "spark.balance_check", 1);
        callback(null, context);
      } catch (error) {
        console.error("Balance check failed:", error.message);
        ee.emit("counter", "spark.balance_check_failed", 1);
        callback(error);
      }
    };
  }

  getIdentityPublicKey(params?: { walletName?: string; storeAs?: string }): EngineStep {
    const ee = this.ee;
    return async function (context: SparkContext, callback) {
      try {
        const walletInfo = params?.walletName ? context.vars?.[params.walletName] : context.sparkWallet;

        if (!walletInfo || !walletInfo.wallet) {
          throw new Error(`Wallet ${params?.walletName || "default"} not found. Initialize it first with initWallet`);
        }

        const publicKeyHex = await walletInfo.wallet.getIdentityPublicKey();

        if (params?.storeAs) {
          context.vars = context.vars || {};
          context.vars[params.storeAs] = publicKeyHex;
        }

        console.log(
          `${walletInfo.name || params?.walletName || "Wallet"} identity public key: ${publicKeyHex}${params?.storeAs ? ` (stored as ${params.storeAs})` : ""}`
        );

        ee.emit("counter", "spark.identity_pubkey_retrieved", 1);
        callback(null, context);
      } catch (error) {
        console.error("Get identity public key failed:", error.message);
        ee.emit("counter", "spark.identity_pubkey_failed", 1);
        callback(error);
      }
    };
  }

  selectWallets(params?: {
    wallets?: Array<{
      walletName: string;
      pool?: string;
      lock?: boolean;
      minBalance?: number;
    }>;
  }): EngineStep {
    return async function (context: SparkContext, callback) {
      try {
        console.log(`SelectWallets: Selecting wallets from pools...`);
        console.log(`  Available pools: ${Array.from(walletPools.keys()).join(", ") || "none"}`);

        const walletConfigs = params?.wallets || [];
        if (walletConfigs.length === 0) {
          console.log(`SelectWallets: No wallets to select`);
          callback(null, context);
          return;
        }

        const selectedIndices: Map<string, Set<number>> = new Map();

        const vuId = context.vars?.__vuId || `vu-${Date.now()}`;

        for (const walletConfig of walletConfigs) {
          const { walletName, pool: poolName = "transfer-pool", lock = false, minBalance = 0 } = walletConfig;

          const pool = walletPools.get(poolName);
          if (!pool || pool.wallets.length === 0) {
            throw new Error(`Pool "${poolName}" not found or empty`);
          }

          if (!selectedIndices.has(poolName)) {
            selectedIndices.set(poolName, new Set());
          }

          const poolSelectedIndices = selectedIndices.get(poolName)!;

          const { isLocked } = await import("./hooks");

          const availableWallets: { wallet: IssuerSparkWallet; index: number; address: string; balance: bigint }[] = [];

          console.log(
            `  Checking wallets in pool "${poolName}" for "${walletName}" (minimum balance: ${minBalance} sats)...`
          );

          for (let i = 0; i < pool.wallets.length; i++) {
            if (!poolSelectedIndices.has(i)) {
              const wallet = pool.wallets[i];
              const address = await wallet.getSparkAddress();

              if (!(await isLocked(address))) {
                if (minBalance > 0) {
                  try {
                    const balanceInfo = await safeGetBalance(wallet);
                    const balance = balanceInfo.balance || 0n;

                    if (balance >= BigInt(minBalance)) {
                      availableWallets.push({ wallet, index: i, address, balance });
                      console.log(`    Wallet ${address.substring(0, 10)}... has ${balance} sats (eligible)`);
                    } else {
                      console.log(`    Wallet ${address.substring(0, 10)}... has ${balance} sats (below minimum)`);
                    }
                  } catch (error) {
                    console.error(
                      `    Failed to check balance for wallet ${address.substring(0, 10)}...: ${error.message}`
                    );
                  }
                } else {
                  availableWallets.push({ wallet, index: i, address, balance: 0n });
                }
              }
            }
          }

          if (availableWallets.length === 0) {
            let lockedCount = 0;
            let insufficientBalanceCount = 0;

            for (const wallet of pool.wallets) {
              const address = await wallet.getSparkAddress();
              if (await isLocked(address)) {
                lockedCount++;
              } else if (minBalance > 0) {
                try {
                  const balanceInfo = await safeGetBalance(wallet);
                  const balance = balanceInfo.balance || 0n;
                  if (balance < BigInt(minBalance)) {
                    insufficientBalanceCount++;
                  }
                } catch (error) {
                  insufficientBalanceCount++;
                }
              }
            }

            let errorMsg = `Not enough eligible wallets in pool "${poolName}" for "${walletName}". `;
            errorMsg += `Pool has ${pool.wallets.length} wallets: `;
            errorMsg += `${lockedCount} are locked, `;
            if (minBalance > 0) {
              errorMsg += `${insufficientBalanceCount} have balance < ${minBalance} sats, `;
            }
            errorMsg += `and ${poolSelectedIndices.size} are already selected in this scenario.`;

            throw new Error(errorMsg);
          }

          const randomIndex = Math.floor(Math.random() * availableWallets.length);
          const selected = availableWallets[randomIndex];
          const selectedWallet = selected.wallet;
          const address = selected.address;

          poolSelectedIndices.add(selected.index);
          const publicKey = await selectedWallet.getIdentityPublicKey();

          if (lock) {
            const lockMetadata = {
              walletName,
              poolName,
              scenarioId: vuId,
              lockedBy: "selectWallets",
              timestamp: new Date().toISOString(),
            };

            if (await createLockFile(address, lockMetadata)) {
              console.log(
                `  Selected (random) and LOCKED wallet from pool "${poolName}" as "${walletName}" (${address.substring(0, 10)}..., balance: ${selected.balance} sats)`
              );

              context.scenarioLockedWallets = context.scenarioLockedWallets || [];
              context.scenarioLockedWallets.push(walletName);

              lockedWallets.set(walletName, { wallet: selectedWallet, pool: poolName });
            } else {
              throw new Error(`Failed to lock wallet "${walletName}" - it may have been locked by another process`);
            }
          } else {
            console.log(
              `  Selected (random) wallet from pool "${poolName}" as "${walletName}" (${address.substring(0, 10)}..., balance: ${selected.balance} sats)`
            );
          }

          context.vars = context.vars || {};
          context.vars[walletName] = {
            wallet: selectedWallet,
            name: walletName,
            address,
            publicKey,
            balance: 0n,
          };
        }

        console.log(`SelectWallets: Successfully selected ${walletConfigs.length} wallets`);
        callback(null, context);
      } catch (error) {
        console.error("SelectWallets failed:", error);
        callback(error);
      }
    };
  }

  private async loadMnemonicsFromFile(filePath: string): Promise<string[]> {
    const fs = await import("fs");
    const fullPath = path.resolve(process.cwd(), filePath);

    if (!fs.existsSync(fullPath)) {
      throw new Error(`Mnemonics file not found: ${fullPath}`);
    }

    const fileContent = fs.readFileSync(fullPath, "utf-8");
    const lines = fileContent.split("\n").filter((line) => line.trim() !== "");
    console.log(`  Loaded ${lines.length} mnemonics from ${filePath}`);

    return lines;
  }

  private async findAvailableWallet(
    mnemonics: string[],
    startIndex: number,
    network: any,
    poolName: string,
    minBalance?: number,
    namedWallet?: { name: string; lock?: boolean; minBalance?: number },
    walletStateDir?: string,
    useRandomSelection: boolean = true,
    skipBackgroundStream?: boolean
  ): Promise<{
    wallet: WalletType;
    index: number;
    address: string;
    balance: bigint;
    publicKey: Uint8Array;
  } | null> {
    return this.findAvailableWalletAtomic(
      mnemonics,
      startIndex,
      network,
      poolName,
      minBalance,
      namedWallet,
      walletStateDir,
      useRandomSelection,
      skipBackgroundStream
    );
  }

  private async findAvailableWalletAtomic(
    mnemonics: string[],
    startIndex: number,
    network: any,
    poolName: string,
    minBalance?: number,
    namedWallet?: { name: string; lock?: boolean; minBalance?: number },
    walletStateDir?: string,
    useRandomSelection: boolean = true,
    skipBackgroundStream?: boolean
  ): Promise<{
    wallet: WalletType;
    index: number;
    address: string;
    balance: bigint;
    publicKey: Uint8Array;
  } | null> {
    console.log(
      `      findAvailableWalletAtomic called with startIndex=${startIndex}, mnemonics.length=${mnemonics.length}, useRandomSelection=${useRandomSelection}`
    );

    if (mnemonics.length === 0) {
      console.error(`      ERROR: No mnemonics provided to findAvailableWalletAtomic`);
      return null;
    }

    if (startIndex >= mnemonics.length) {
      console.log(`      startIndex ${startIndex} is beyond mnemonics array length ${mnemonics.length}, wrapping to 0`);
      startIndex = 0;
    }
    const { tryLockOneOf, removeLockFile } = await import("./hooks");
    const BATCH_SIZE = 3;

    console.log(`    Using atomic wallet selection (random: ${useRandomSelection}) from index ${startIndex}...`);

    let searchedIndices = 0;
    let currentStart = startIndex;
    let wrapped = false;
    const maxSearchAttempts = Math.ceil(mnemonics.length / BATCH_SIZE) * 2;
    let searchAttempts = 0;

    while (searchedIndices < mnemonics.length && searchAttempts < maxSearchAttempts) {
      searchAttempts++;

      if (currentStart >= mnemonics.length) {
        if (wrapped) {
          console.log(`    Completed full wrap-around search`);
          break;
        }
        console.log(`    Reached end of mnemonics list at index ${currentStart}, wrapping around to beginning...`);
        currentStart = 0;
        wrapped = true;
      }

      const maxEnd = wrapped ? startIndex : mnemonics.length;
      const batchEnd = Math.min(currentStart + BATCH_SIZE, maxEnd);

      if (currentStart >= batchEnd || (wrapped && currentStart >= startIndex)) {
        console.log(`    Search complete - wrapped around to starting point`);
        break;
      }

      const batchIndices = [];

      for (let i = currentStart; i < batchEnd; i++) {
        batchIndices.push(i);
      }

      searchedIndices += batchIndices.length;

      if (useRandomSelection) {
        for (let i = batchIndices.length - 1; i > 0; i--) {
          const j = Math.floor(Math.random() * (i + 1));
          [batchIndices[i], batchIndices[j]] = [batchIndices[j], batchIndices[i]];
        }
      }

      const lockKeys = batchIndices.map((idx) => `${poolName}_wallet_${idx}`);

      const lockMetadata = {
        walletName: namedWallet?.name || `${poolName}_wallet`,
        poolName,
        lockedBy: "findAvailableWalletAtomic",
        timestamp: new Date().toISOString(),
        pid: process.pid,
      };

      const lockedKey = await tryLockOneOf(lockKeys, lockMetadata);

      if (lockedKey) {
        const lockedIndex = parseInt(lockedKey.split("_").pop() || "0", 10);
        const mnemonic = mnemonics[lockedIndex]?.trim();

        if (!mnemonic) {
          console.warn(`    No mnemonic at locked index ${lockedIndex}, unlocking...`);
          await removeLockFile(lockedKey);
          continue;
        }

        console.log(`    Locked wallet index ${lockedIndex}, initializing...`);

        try {
          console.log(`  Using REGTEST network (loadtest environment)`);
          const networkConfig = getLoadtestNetworkConfig();

          console.log(`      Initializing wallet with mnemonic index ${lockedIndex}...`);
          const initStartTime = Date.now();
          const { wallet } = await initializeWalletWithRetry(() =>
            IssuerSparkWalletNoEvents.initialize({
              skipBackgroundStream,
              options: networkConfig,
              mnemonicOrSeed: mnemonic,
            })
          );

          const initTime = Date.now() - initStartTime;
          console.log(`      Wallet initialized in ${initTime}ms`);

          const address = await wallet.getSparkAddress();
          const publicKeyHex = await wallet.getIdentityPublicKey();
          const publicKey = hexToBytes(publicKeyHex);

          if (minBalance !== undefined && minBalance > 0) {
            const { balance } = await safeGetBalance(wallet);

            if (balance < minBalance) {
              console.log(
                `    Wallet ${lockedIndex} has insufficient balance (${balance} < ${minBalance} sats), unlocking...`
              );
              await removeLockFile(lockedKey);
              continue;
            }

            console.log(
              `    Found and locked wallet ${lockedIndex} (${address.substring(0, 10)}..., balance: ${balance} sats)`
            );
            return {
              wallet,
              index: lockedIndex,
              address,
              balance,
              publicKey,
            };
          } else {
            console.log(`    Found and locked wallet ${lockedIndex} (${address.substring(0, 10)}...)`);
            return {
              wallet,
              index: lockedIndex,
              address,
              balance: 0n,
              publicKey,
            };
          }
        } catch (error) {
          console.error(`    Failed to initialize locked wallet at index ${lockedIndex}: ${error.message}`);
          await removeLockFile(lockedKey);
          continue;
        }
      } else {
        console.log(
          `    All ${lockKeys.length} wallet indices in batch ${currentStart}-${batchEnd} are locked, moving to next batch...`
        );
      }

      currentStart = batchEnd;
    }

    if (searchAttempts >= maxSearchAttempts) {
      console.log(`    Exhausted search attempts (${searchAttempts}) - too many locked wallets`);
    }

    console.log(
      `    No available wallets found after ${searchAttempts} attempts, searched ${searchedIndices} indices (started from index ${startIndex})`
    );
    return null;
  }

  initializePools(params?: {
    pools?: Array<{
      name: string;
      amount?: number;
      amountEnvName?: string;
      mnemonicsFile?: string;
      batchSize?: number;
      randomSelection?: boolean;
      lockWallets?: boolean;
      minBalance?: number;
      useWalletStateCache?: boolean;
      skipBackgroundStream?: boolean;
      namedWallets?: Array<{
        name: string;
        lock?: boolean;
        minBalance?: number;
      }>;
    }>;
  }): EngineStep {
    const self = this;
    return async function (context: SparkContext, callback) {
      const startTime = Date.now();
      let totalWalletsInitialized = 0;
      let totalPoolsCreated = 0;

      try {
        console.log(`InitializePools: Starting wallet pool initialization...`);
        console.log(`  Parameters:`, JSON.stringify(params, null, 2));

        const network = (process.env.SPARK_NETWORK || "LOCAL") as
          | "MAINNET"
          | "REGTEST"
          | "TESTNET"
          | "SIGNET"
          | "LOCAL";

        const isLoadtestEnvironment = true;
        const effectiveNetwork = network === "LOCAL" && isLoadtestEnvironment ? "REGTEST" : network;

        const poolConfigs = params?.pools || [];

        if (poolConfigs.length === 0) {
          console.log(`InitializePools: No pools specified, skipping initialization`);
          callback(null, context);
          return;
        }

        const vuId = context.vars?.__vuId || `vu-${Date.now()}`;

        const poolInitPromises = poolConfigs.map(async (poolConfig) => {
          const poolName = poolConfig.name;
          const minBalance = poolConfig.minBalance;
          const namedWallets = poolConfig.namedWallets || [];
          const useWalletStateCache = poolConfig.useWalletStateCache || false;

          if (walletPools.has(poolName)) {
            console.log(`Pool "${poolName}" already exists, re-establishing named wallets in context...`);

            const existingPool = walletPools.get(poolName);
            if (!existingPool) {
              return { poolName, successfulWallets: 0, totalPoolsCreated: 0 };
            }

            if (namedWallets.length > 0) {
              for (const namedWallet of namedWallets) {
                const lockedWallet = lockedWallets.get(namedWallet.name);
                if (lockedWallet && lockedWallet.pool === poolName) {
                  const address = await lockedWallet.wallet.getSparkAddress();
                  const publicKey = await lockedWallet.wallet.getIdentityPublicKey();
                  const balance = await lockedWallet.wallet.getBalance();

                  context.vars = context.vars || {};
                  context.vars[namedWallet.name] = {
                    wallet: lockedWallet.wallet,
                    name: namedWallet.name,
                    address: address,
                    publicKey: typeof publicKey === "string" ? publicKey : bytesToHex(publicKey),
                    balance: balance,
                  };
                  console.log(
                    `  Re-established locked wallet "${namedWallet.name}" in scenario context (address: ${address.substring(0, 10)}...)`
                  );
                } else {
                  console.log(
                    `  Named wallet "${namedWallet.name}" not found in locked wallets, initializing new wallet...`
                  );
                  let mnemonics: string[] = [];
                  if (poolConfig.mnemonicsFile) {
                    try {
                      mnemonics = await self.loadMnemonicsFromFile(poolConfig.mnemonicsFile);
                      console.log(`    Loaded ${mnemonics.length} mnemonics for finding unlocked wallet`);
                    } catch (error) {
                      console.error(`    Failed to load mnemonics: ${error}`);
                      continue;
                    }
                  }

                  const network = (process.env.SPARK_NETWORK || "LOCAL") as
                    | "MAINNET"
                    | "REGTEST"
                    | "TESTNET"
                    | "SIGNET"
                    | "LOCAL";
                  const effectiveNetwork = network === "LOCAL" ? "REGTEST" : network;

                  const startIndex = Math.floor(Math.random() * mnemonics.length);
                  const walletInfo = await self.findAvailableWallet(
                    mnemonics,
                    startIndex,
                    effectiveNetwork,
                    poolName,
                    namedWallet.minBalance || poolConfig.minBalance,
                    namedWallet,
                    undefined,
                    true,
                    poolConfig.skipBackgroundStream
                  );

                  if (walletInfo) {
                    console.log(
                      `    Successfully initialized wallet for "${namedWallet.name}" at address: ${walletInfo.address.substring(0, 10)}...`
                    );

                    existingPool.wallets.push(walletInfo.wallet);

                    const lockMetadata = {
                      walletName: namedWallet.name,
                      poolName,
                      scenarioId: vuId,
                      lockedBy: "initializePools",
                      timestamp: new Date().toISOString(),
                    };

                    if (await createLockFile(walletInfo.address, lockMetadata)) {
                      console.log(`    Locked wallet for "${namedWallet.name}"`);

                      lockedWallets.set(namedWallet.name, { wallet: walletInfo.wallet, pool: poolName });

                      context.scenarioLockedWallets = context.scenarioLockedWallets || [];
                      context.scenarioLockedWallets.push(namedWallet.name);
                    }

                    context.vars = context.vars || {};
                    context.vars[namedWallet.name] = {
                      wallet: walletInfo.wallet,
                      name: namedWallet.name,
                      address: walletInfo.address,
                      publicKey: bytesToHex(walletInfo.publicKey),
                      balance: walletInfo.balance,
                    };
                    console.log(
                      `  Established "${namedWallet.name}" in scenario context (address: ${walletInfo.address.substring(0, 10)}...)`
                    );
                  } else {
                    console.error(
                      `  ERROR: Could not initialize wallet for "${namedWallet.name}" - all wallets may be locked`
                    );
                  }
                }
              }
            }

            return { poolName, successfulWallets: 0, totalPoolsCreated: 0 };
          }

          let walletStateDir: string | undefined;
          if (useWalletStateCache) {
            walletStateDir = `.wallet-states/${poolName}`;
            const fs = await import("fs/promises");
            await fs.mkdir(walletStateDir, { recursive: true });
            console.log(`  Created wallet state directory: ${walletStateDir}`);
          }

          let targetAmount: number;
          if (poolConfig.amountEnvName) {
            const envValue = process.env[poolConfig.amountEnvName];
            if (!envValue) {
              throw new Error(`Environment variable ${poolConfig.amountEnvName} not found for pool "${poolName}"`);
            }
            targetAmount = parseInt(envValue, 10);
            if (isNaN(targetAmount) || targetAmount <= 0) {
              throw new Error(`Invalid pool size from env var ${poolConfig.amountEnvName}: ${envValue}`);
            }
          } else if (poolConfig.amount !== undefined) {
            targetAmount = poolConfig.amount;
          } else if (namedWallets.length > 0) {
            targetAmount = namedWallets.length;
          } else {
            targetAmount = parseInt(process.env.SPARK_DEFAULT_POOL_SIZE || "50");
          }

          console.log(`\nInitializing pool "${poolName}"`);
          console.log(`  Target wallets: ${targetAmount}`);
          if (minBalance) {
            console.log(`  Minimum balance required: ${minBalance} sats`);
          }
          if (namedWallets.length > 0) {
            console.log(`  Named wallets: ${namedWallets.map((w) => w.name).join(", ")}`);
          }

          const wallets: IssuerSparkWallet[] = [];
          const available: IssuerSparkWallet[] = [];

          let mnemonics: string[] = [];
          if (poolConfig.mnemonicsFile) {
            console.log(`  Loading mnemonics from: ${poolConfig.mnemonicsFile}`);
            try {
              mnemonics = await self.loadMnemonicsFromFile(poolConfig.mnemonicsFile);
              console.log(`  Successfully loaded ${mnemonics.length} mnemonics`);
            } catch (error) {
              console.error(`  Failed to load mnemonics from ${poolConfig.mnemonicsFile}:`, error);
              throw error;
            }
          } else {
            console.log(`  No mnemonics file specified for pool "${poolName}"`);
          }

          let mnemonicIndex = 0;
          let successfulWallets = 0;
          let lockedCount = 0;

          const batchSize = poolConfig.batchSize || 5;
          console.log(`  Using batch size: ${batchSize} for parallel wallet initialization`);

          if (namedWallets.length > 0) {
            for (let i = 0; i < namedWallets.length && successfulWallets < targetAmount; i += batchSize) {
              const batch = namedWallets.slice(i, Math.min(i + batchSize, namedWallets.length));

              const batchPromises = batch.map(async (namedWallet) => {
                if (successfulWallets >= targetAmount) return null;

                const walletMinBalance = namedWallet.minBalance || minBalance;

                try {
                  console.log(
                    `    Attempting to initialize wallet for "${namedWallet.name}" from pool "${poolName}" (index: ${mnemonicIndex}/${mnemonics.length})`
                  );
                  const walletInfo = await self.findAvailableWallet(
                    mnemonics,
                    mnemonicIndex,
                    effectiveNetwork,
                    poolName,
                    namedWallet.minBalance || minBalance,
                    namedWallet,
                    walletStateDir,
                    poolConfig.randomSelection !== false,
                    poolConfig.skipBackgroundStream
                  );

                  if (!walletInfo) {
                    const requiredBalance = namedWallet.minBalance || minBalance;
                    console.error(`    ERROR: findAvailableWallet returned null for "${namedWallet.name}"`);
                    console.error(
                      `    Pool: ${poolName}, mnemonics: ${mnemonics.length}, startIndex: ${mnemonicIndex}`
                    );
                    throw new Error(
                      `Could not find available wallet for "${namedWallet.name}" in pool "${poolName}"${requiredBalance ? ` with minimum balance ${requiredBalance} sats` : ""}`
                    );
                  }

                  console.log(
                    `    Successfully found wallet for "${namedWallet.name}" at address: ${walletInfo.address.substring(0, 10)}...`
                  );
                  return { walletInfo, namedWallet };
                } catch (error) {
                  console.error(`Failed to initialize wallet for "${namedWallet.name}":`, error);
                  throw error;
                }
              });

              const batchResults = await Promise.all(batchPromises);
              console.log(`    Batch completed with ${batchResults.length} results`);

              for (const result of batchResults) {
                if (!result) {
                  console.log(`    Skipping null result in batch`);
                  continue;
                }

                const { walletInfo, namedWallet } = result;
                mnemonicIndex = Math.max(mnemonicIndex, walletInfo.index + 1);
                if (mnemonicIndex >= mnemonics.length) {
                  mnemonicIndex = 0;
                }
                wallets.push(walletInfo.wallet);
                successfulWallets++;

                if (namedWallet.lock || poolConfig.lockWallets) {
                  const lockMetadata = {
                    walletName: namedWallet.name,
                    poolName,
                    scenarioId: vuId,
                    lockedBy: "initializePools",
                    timestamp: new Date().toISOString(),
                  };

                  if (await createLockFile(walletInfo.address, lockMetadata)) {
                    lockedCount++;
                    console.log(`    Locked wallet for "${namedWallet.name}"`);

                    lockedWallets.set(namedWallet.name, { wallet: walletInfo.wallet, pool: poolName });

                    context.scenarioLockedWallets = context.scenarioLockedWallets || [];
                    context.scenarioLockedWallets.push(namedWallet.name);
                  }
                } else {
                  available.push(walletInfo.wallet);
                }

                context.vars = context.vars || {};
                context.vars[namedWallet.name] = {
                  wallet: walletInfo.wallet,
                  name: namedWallet.name,
                  address: walletInfo.address,
                  publicKey: bytesToHex(walletInfo.publicKey),
                  balance: walletInfo.balance,
                };
                console.log(
                  `    Added "${namedWallet.name}" to scenario context (address: ${walletInfo.address.substring(0, 10)}...)`
                );
              }
            }
          }

          while (successfulWallets < targetAmount && mnemonicIndex < mnemonics.length) {
            const remainingNeeded = targetAmount - successfulWallets;
            const currentBatchSize = Math.min(batchSize, remainingNeeded);

            const batchPromises = [];
            for (let i = 0; i < currentBatchSize && mnemonicIndex < mnemonics.length; i++) {
              const currentIndex = mnemonicIndex + i;

              batchPromises.push(
                self
                  .findAvailableWallet(
                    mnemonics,
                    currentIndex,
                    effectiveNetwork,
                    poolName,
                    minBalance,
                    undefined,
                    walletStateDir,
                    poolConfig.randomSelection !== false,
                    poolConfig.skipBackgroundStream
                  )
                  .then((walletInfo) => ({ walletInfo, attemptedIndex: currentIndex }))
                  .catch((error) => {
                    console.warn(`Failed to initialize wallet at index ${currentIndex}:`, error);
                    return { walletInfo: null, attemptedIndex: currentIndex };
                  })
              );
            }

            const batchResults = await Promise.all(batchPromises);

            const attemptedIndices = batchResults.filter((r) => r !== null).map((r) => r.attemptedIndex);

            if (attemptedIndices.length > 0) {
              const maxAttemptedIndex = Math.max(...attemptedIndices);
              mnemonicIndex = maxAttemptedIndex + 1;
            } else {
              mnemonicIndex += currentBatchSize;
            }

            if (mnemonicIndex >= mnemonics.length) {
              mnemonicIndex = 0;
            }

            let batchSuccessCount = 0;
            for (const result of batchResults) {
              if (!result || !result.walletInfo) continue;

              const { walletInfo } = result;
              wallets.push(walletInfo.wallet);
              successfulWallets++;
              batchSuccessCount++;

              if (poolConfig.lockWallets) {
                const walletName = `${poolName}_wallet_${successfulWallets}`;
                const lockMetadata = {
                  walletName,
                  poolName,
                  scenarioId: vuId,
                  lockedBy: "initializePools",
                  timestamp: new Date().toISOString(),
                };

                if (await createLockFile(walletInfo.address, lockMetadata)) {
                  lockedCount++;
                  console.log(`    Locked as "${walletName}"`);

                  lockedWallets.set(walletName, { wallet: walletInfo.wallet, pool: poolName });

                  context.scenarioLockedWallets = context.scenarioLockedWallets || [];
                  context.scenarioLockedWallets.push(walletName);
                }
              } else {
                available.push(walletInfo.wallet);
              }
            }

            if (batchSuccessCount === 0) {
              console.warn(
                `  Could not find more available wallets. mnemonicIndex=${mnemonicIndex}, mnemonics.length=${mnemonics.length}`
              );
              break;
            }

            if (successfulWallets < targetAmount && mnemonicIndex < mnemonics.length) {
              await new Promise((resolve) => setTimeout(resolve, 100));
            }
          }

          walletPools.set(poolName, { wallets, available });

          console.log(`\n  Pool "${poolName}" initialization summary:`);
          console.log(`    Target wallets: ${targetAmount}`);
          console.log(`    Successfully initialized: ${successfulWallets}`);
          console.log(`    Available (unlocked): ${available.length}`);
          console.log(`    Locked by this session: ${lockedCount}`);
          if (useWalletStateCache) {
            console.log(`    Wallet state caching: ENABLED (${walletStateDir})`);
          }
          if (minBalance) {
            console.log(`    All wallets have at least ${minBalance} sats`);
          }
          if (mnemonics.length > 0) {
            console.log(`    Checked ${mnemonicIndex} of ${mnemonics.length} mnemonics from file`);
          }

          return { poolName, successfulWallets, totalPoolsCreated: 1 };
        });

        const poolResults = await Promise.all(poolInitPromises);

        poolResults.forEach((result) => {
          totalWalletsInitialized += result.successfulWallets;
          totalPoolsCreated += result.totalPoolsCreated;
        });

        console.log(`\nInitializePools: Created ${walletPools.size} pools`);

        const initTime = Date.now() - startTime;
        console.log(`Pool initialization took ${initTime}ms`);

        let totalWalletsInPools = 0;
        for (const pool of walletPools.values()) {
          if (pool && pool.wallets) {
            totalWalletsInPools += pool.wallets.length;
          }
        }

        const scenarioEE = self.engine?.scenarioEE;
        if (scenarioEE) {
          console.log(
            `Emitting metrics to scenario EE: initialize_pools_time=${initTime}, pools_created=${totalPoolsCreated}, wallets_initialized=${totalWalletsInitialized}`
          );
          scenarioEE.emit("histogram", "spark.initialize_pools_time", initTime);
          scenarioEE.emit("counter", "spark.pools_created", totalPoolsCreated);
          scenarioEE.emit("counter", "spark.wallets_initialized", totalWalletsInitialized);
          scenarioEE.emit("counter", "spark.initialize_pools_success", 1);
        } else {
          self.ee.emit("histogram", "spark.initialize_pools_time", initTime);
          self.ee.emit("counter", "spark.pools_created", totalPoolsCreated);
          self.ee.emit("counter", "spark.wallets_initialized", totalWalletsInitialized);
          self.ee.emit("counter", "spark.initialize_pools_success", 1);
        }

        callback(null, context);
      } catch (error) {
        console.error("InitializePools failed:", error);

        const scenarioEE = self.engine?.scenarioEE;
        if (scenarioEE) {
          scenarioEE.emit("counter", "spark.initialize_pools_failed", 1);
        } else {
          self.ee.emit("counter", "spark.initialize_pools_failed", 1);
        }

        callback(error);
      }
    };
  }

  setTransferAmount(params?: { amount?: number; min?: number; max?: number; storeAs?: string }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      try {
        let amount: number;

        if (params?.amount !== undefined) {
          amount = params.amount;
        } else if (params?.min !== undefined && params?.max !== undefined) {
          amount = Math.floor(Math.random() * (params.max - params.min + 1)) + params.min;
        } else {
          amount = Math.floor(Math.random() * 99001) + 1000;
        }

        context.vars = context.vars || {};
        const key = params?.storeAs || "transferAmount";
        context.vars[key] = amount;

        console.log(`Transfer amount set to ${amount} sats (stored as ${key})`);

        ee.emit("counter", "spark.transfer_amount_set", 1);
        callback(null, context);
      } catch (error) {
        console.error("Failed to set transfer amount:", error.message);
        callback(error);
      }
    };
  }

  collectFundsToPool(params?: { targetPool: string; sourcePools?: string[]; leaveAmount?: number }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      try {
        if (!params?.targetPool) {
          throw new Error("targetPool is required for collectFundsToPool");
        }

        const targetPool = walletPools.get(params.targetPool);
        if (!targetPool || targetPool.wallets.length === 0) {
          throw new Error(`Target pool "${params.targetPool}" not found or empty`);
        }

        let sourcePools: string[];
        if (params.sourcePools && params.sourcePools.length > 0) {
          sourcePools = params.sourcePools;
        } else {
          sourcePools = Array.from(walletPools.keys()).filter((name) => name !== params.targetPool);
        }

        const leaveAmount = params.leaveAmount || 0;

        console.log(
          `CollectFundsToPool: Collecting funds from pools [${sourcePools.join(", ")}] to "${params.targetPool}"`
        );
        if (leaveAmount > 0) {
          console.log(`  Leaving ${leaveAmount} sats in each source wallet`);
        }

        if (targetPool.available.length === 0) {
          throw new Error(`No available wallets in target pool "${params.targetPool}"`);
        }

        const collectorWallet = targetPool.available[0];
        const collectorAddress = await collectorWallet.getSparkAddress();

        console.log(`  Using collector wallet: ${collectorAddress.substring(0, 10)}...`);

        let totalCollected = 0;
        let walletsProcessed = 0;

        for (const poolName of sourcePools) {
          const pool = walletPools.get(poolName);
          if (!pool) {
            console.warn(`  Pool "${poolName}" not found, skipping...`);
            continue;
          }

          console.log(`  Processing ${pool.wallets.length} wallets from pool "${poolName}"...`);

          for (const wallet of pool.wallets) {
            try {
              const walletAddress = await wallet.getSparkAddress();
              if (walletAddress === collectorAddress) {
                continue;
              }

              const balanceInfo = await safeGetBalance(wallet);
              const balance = balanceInfo.balance || 0n;

              const transferAmount = balance > BigInt(leaveAmount) ? balance - BigInt(leaveAmount) : 0n;

              if (transferAmount > 0) {
                console.log(`    Transferring ${transferAmount} sats from ${walletAddress.substring(0, 10)}...`);

                try {
                  const transferResult = await wallet.transfer({
                    amountSats: Number(transferAmount),
                    receiverSparkAddress: collectorAddress,
                  });

                  console.log(`    ✓ Transfer successful (ID: ${transferResult.id})`);
                  totalCollected += Number(transferAmount);
                  walletsProcessed++;
                } catch (error) {
                  console.error(`    ✗ Failed to transfer from ${walletAddress.substring(0, 10)}...: ${error.message}`);
                }
              } else {
                console.log(`    Skipping ${walletAddress.substring(0, 10)}... (balance: ${balance} sats)`);
              }
            } catch (error) {
              console.error(`    Error processing wallet: ${error.message}`);
            }
          }
        }

        try {
          const finalBalance = await safeGetBalance(collectorWallet);
          console.log(`  Collector wallet final balance: ${finalBalance.balance} sats`);
        } catch (error) {
          console.warn(`  Could not get final collector balance: ${error.message}`);
        }

        console.log(`CollectFundsToPool: Collected ${totalCollected} sats from ${walletsProcessed} wallets`);

        context.vars = context.vars || {};
        context.vars.totalCollected = totalCollected;
        context.vars.walletsProcessed = walletsProcessed;

        ee.emit("counter", "spark.funds_collected", totalCollected);
        ee.emit("counter", "spark.wallets_collected_from", walletsProcessed);

        callback(null, context);
      } catch (error) {
        console.error("Fund collection failed:", error.message);
        ee.emit("counter", "spark.fund_collection_failed", 1);
        callback(error);
      }
    };
  }

  transferPoolFunds(params?: {
    sourcePool: string;
    targetPool: string;
    leaveAmount?: number;
    distributeEvenly?: boolean;
  }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      try {
        if (!params?.sourcePool || !params?.targetPool) {
          throw new Error("Both sourcePool and targetPool are required for transferPoolFunds");
        }

        const sourcePool = walletPools.get(params.sourcePool);
        const targetPool = walletPools.get(params.targetPool);

        if (!sourcePool || sourcePool.wallets.length === 0) {
          throw new Error(`Source pool "${params.sourcePool}" not found or empty`);
        }

        if (!targetPool || targetPool.wallets.length === 0) {
          throw new Error(`Target pool "${params.targetPool}" not found or empty`);
        }

        const leaveAmount = params.leaveAmount || 0;
        const distributeEvenly = params.distributeEvenly || false;

        let totalAvailable = 0n;
        const sourceBalances: Map<string, { wallet: IssuerSparkWallet; balance: bigint }> = new Map();

        for (const wallet of sourcePool.wallets) {
          try {
            const address = await wallet.getSparkAddress();
            const balanceInfo = await safeGetBalance(wallet);
            const balance = balanceInfo.balance || 0n;
            const transferable = balance > BigInt(leaveAmount) ? balance - BigInt(leaveAmount) : 0n;

            if (transferable > 0) {
              sourceBalances.set(address, { wallet, balance: transferable });
              totalAvailable += transferable;
            }
          } catch (error) {}
        }

        if (totalAvailable === 0n) {
          callback(null, context);
          return;
        }

        const targetWallets: Array<{ wallet: IssuerSparkWallet; address: string }> = [];
        for (const wallet of targetPool.wallets) {
          const address = await wallet.getSparkAddress();
          targetWallets.push({ wallet, address });
        }

        let totalTransferred = 0;
        let transferCount = 0;

        if (distributeEvenly) {
          const amountPerWallet = totalAvailable / BigInt(targetWallets.length);
          const remainder = totalAvailable % BigInt(targetWallets.length);

          const transfers: Array<{ from: IssuerSparkWallet; to: string; amount: bigint }> = [];
          let targetIndex = 0;
          let remainingAmount = totalAvailable;

          for (const [, { wallet: sourceWallet, balance }] of sourceBalances) {
            let walletRemaining = balance;

            while (walletRemaining > 0n && targetIndex < targetWallets.length) {
              const targetWallet = targetWallets[targetIndex];
              const targetAmount = targetIndex < Number(remainder) ? amountPerWallet + 1n : amountPerWallet;
              const transferAmount = walletRemaining < targetAmount ? walletRemaining : targetAmount;

              transfers.push({
                from: sourceWallet,
                to: targetWallet.address,
                amount: transferAmount,
              });

              walletRemaining -= transferAmount;
              remainingAmount -= transferAmount;

              if (remainingAmount <= BigInt(targetWallets.length - targetIndex - 1) * amountPerWallet) {
                targetIndex++;
              }
            }
          }

          for (const transfer of transfers) {
            try {
              const fromAddress = await transfer.from.getSparkAddress();

              const result = await transfer.from.transfer({
                amountSats: Number(transfer.amount),
                receiverSparkAddress: transfer.to,
              });

              totalTransferred += Number(transfer.amount);
              transferCount++;
            } catch (error) {}
          }
        } else {
          const collectorWallet = targetWallets[0];

          for (const [address, { wallet: sourceWallet, balance }] of sourceBalances) {
            if (balance > 0) {
              try {
                const result = await sourceWallet.transfer({
                  amountSats: Number(balance),
                  receiverSparkAddress: collectorWallet.address,
                });

                totalTransferred += Number(balance);
                transferCount++;
              } catch (error) {}
            }
          }
        }

        context.vars = context.vars || {};
        context.vars.poolTransferTotal = totalTransferred;
        context.vars.poolTransferCount = transferCount;

        ee.emit("counter", "spark.pool_funds_transferred", totalTransferred);
        ee.emit("counter", "spark.pool_transfers_completed", transferCount);

        callback(null, context);
      } catch (error) {
        ee.emit("counter", "spark.pool_transfer_failed", 1);
        callback(error);
      }
    };
  }

  unlockPoolWallets(params?: { pools?: string[]; force?: boolean }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      try {
        const poolsToUnlock = params?.pools || Array.from(walletPools.keys());
        const forceUnlock = params?.force || false;

        console.log(`UnlockPoolWallets: Unlocking wallets from pools: [${poolsToUnlock.join(", ")}]`);
        if (forceUnlock) {
          console.log(`  Force mode enabled - will unlock all wallets regardless of owner`);
        }

        let unlockedCount = 0;
        let failedCount = 0;

        for (const poolName of poolsToUnlock) {
          const pool = walletPools.get(poolName);
          if (!pool) {
            console.warn(`  Pool "${poolName}" not found, skipping...`);
            continue;
          }

          console.log(`  Processing pool "${poolName}" with ${pool.wallets.length} wallets...`);

          for (const wallet of pool.wallets) {
            try {
              const address = await wallet.getSparkAddress();

              if (await isLocked(address)) {
                if (!forceUnlock) {
                  // TODO: Add scenario ownership check to lock manager
                  console.log(`    Skipping ${address.substring(0, 10)}... - locked`);
                  continue;
                }

                if (await removeLockFile(address)) {
                  console.log(`    Unlocked wallet ${address.substring(0, 10)}...`);
                  unlockedCount++;

                  for (const [walletName, lockedInfo] of lockedWallets) {
                    if (lockedInfo.wallet === wallet) {
                      lockedWallets.delete(walletName);

                      if (!pool.available.includes(wallet)) {
                        pool.available.push(wallet);
                      }
                      break;
                    }
                  }
                } else {
                  console.error(`    Failed to unlock wallet ${address.substring(0, 10)}...`);
                  failedCount++;
                }
              }
            } catch (error) {
              console.error(`    Error processing wallet: ${error.message}`);
              failedCount++;
            }
          }
        }

        console.log(`UnlockPoolWallets: Unlocked ${unlockedCount} wallets`);
        if (failedCount > 0) {
          console.warn(`  Failed to unlock ${failedCount} wallets`);
        }

        ee.emit("counter", "spark.wallets_unlocked", unlockedCount);
        if (failedCount > 0) {
          ee.emit("counter", "spark.wallets_unlock_failed", failedCount);
        }

        callback(null, context);
      } catch (error) {
        console.error("Unlock pool wallets failed:", error.message);
        ee.emit("counter", "spark.unlock_pool_failed", 1);
        callback(error);
      }
    };
  }

  transferAllFunds(params?: {
    sourceMnemonicsFile: string;
    targetMnemonicsFile: string;
    leaveAmount?: number;
  }): EngineStep {
    const ee = this.ee;
    const self = this;

    return async function (context: SparkContext, callback) {
      try {
        if (!params?.sourceMnemonicsFile || !params?.targetMnemonicsFile) {
          throw new Error("Both sourceMnemonicsFile and targetMnemonicsFile are required");
        }

        const leaveAmount = params.leaveAmount || 0;

        console.log(`TransferAllFunds: Loading source wallets from ${params.sourceMnemonicsFile}...`);

        const sourceMnemonics = await self.loadMnemonicsFromFile(params.sourceMnemonicsFile);
        console.log(`  Loaded ${sourceMnemonics.length} source mnemonics`);

        const targetMnemonics = await self.loadMnemonicsFromFile(params.targetMnemonicsFile);
        console.log(`  Loaded ${targetMnemonics.length} target mnemonics`);

        console.log(`  Using REGTEST network (loadtest environment)`);
        const networkConfig = getLoadtestNetworkConfig();

        console.log(`  Initializing ${sourceMnemonics.length} source wallets in batches of 50...`);
        const sourceWallets: { wallet: IssuerSparkWallet; mnemonic: string; address: string }[] = [];
        const sourceInitStartTime = Date.now();
        const BATCH_SIZE = 50;

        for (let batchStart = 0; batchStart < sourceMnemonics.length; batchStart += BATCH_SIZE) {
          const batchEnd = Math.min(batchStart + BATCH_SIZE, sourceMnemonics.length);
          const batchNumber = Math.floor(batchStart / BATCH_SIZE) + 1;
          const totalBatches = Math.ceil(sourceMnemonics.length / BATCH_SIZE);

          console.log(`    Processing batch ${batchNumber}/${totalBatches} (wallets ${batchStart + 1}-${batchEnd})...`);
          const batchStartTime = Date.now();

          const batchPromises = [];
          for (let i = batchStart; i < batchEnd; i++) {
            const mnemonic = sourceMnemonics[i];
            const walletIndex = i + 1;

            batchPromises.push(
              (async () => {
                try {
                  const { wallet } = await initializeWalletWithRetry(() =>
                    IssuerSparkWallet.initialize({
                      mnemonicOrSeed: mnemonic,
                      options: networkConfig,
                    })
                  );

                  const address = await wallet.getSparkAddress();
                  return { wallet, mnemonic, address, index: walletIndex };
                } catch (error) {
                  console.error(`      Failed to initialize source wallet ${walletIndex}: ${error.message}`);
                  return null;
                }
              })()
            );
          }

          const batchResults = await Promise.all(batchPromises);

          for (const result of batchResults) {
            if (result) {
              sourceWallets.push({ wallet: result.wallet, mnemonic: result.mnemonic, address: result.address });
            }
          }

          const batchEndTime = Date.now();
          const batchTime = batchEndTime - batchStartTime;
          const avgTimePerWallet = Math.floor(batchTime / (batchEnd - batchStart));

          console.log(`    Batch ${batchNumber} completed in ${batchTime}ms (avg ${avgTimePerWallet}ms/wallet)`);
          console.log(`    Total progress: ${sourceWallets.length}/${sourceMnemonics.length} wallets initialized`);
        }

        const totalInitTime = Date.now() - sourceInitStartTime;
        console.log(`  All ${sourceWallets.length} source wallets initialized in ${Math.floor(totalInitTime / 1000)}s`);

        console.log(`  Getting balances for source wallets in batches...`);
        let totalAvailable = 0n;
        const fundedSourceWallets: { wallet: IssuerSparkWallet; address: string; balance: bigint }[] = [];
        const balanceCheckStartTime = Date.now();

        for (let batchStart = 0; batchStart < sourceWallets.length; batchStart += BATCH_SIZE) {
          const batchEnd = Math.min(batchStart + BATCH_SIZE, sourceWallets.length);
          const batchNumber = Math.floor(batchStart / BATCH_SIZE) + 1;
          const totalBatches = Math.ceil(sourceWallets.length / BATCH_SIZE);

          console.log(`    Checking balances batch ${batchNumber}/${totalBatches}...`);

          const batchPromises = sourceWallets.slice(batchStart, batchEnd).map(async ({ wallet, address }) => {
            try {
              const { balance } = await safeGetBalance(wallet);
              const transferable = balance > BigInt(leaveAmount) ? balance - BigInt(leaveAmount) : 0n;

              if (transferable > 0) {
                console.log(`      ${address.substring(0, 10)}... has ${transferable} sats available`);
                return { wallet, address, balance: transferable };
              }
              return null;
            } catch (error) {
              console.error(`      Failed to get balance for ${address.substring(0, 10)}...: ${error.message}`);
              return null;
            }
          });

          const batchResults = await Promise.all(batchPromises);

          for (const result of batchResults) {
            if (result) {
              fundedSourceWallets.push(result);
              totalAvailable += result.balance;
            }
          }

          const fundedInBatch = batchResults.filter((r) => r !== null).length;
          console.log(`    Batch ${batchNumber} completed: ${fundedInBatch} funded wallets found`);
        }

        const balanceCheckTime = Date.now() - balanceCheckStartTime;
        console.log(
          `  Total available funds: ${totalAvailable} sats from ${fundedSourceWallets.length} wallets (checked in ${Math.floor(balanceCheckTime / 1000)}s)`
        );

        if (totalAvailable === 0n) {
          console.log("  No funds available to transfer");
          callback(null, context);
          return;
        }

        const targetCount = Math.min(targetMnemonics.length, fundedSourceWallets.length);
        console.log(`  Initializing ${targetCount} target wallets...`);

        const targetWallets: { wallet: IssuerSparkWallet; address: string }[] = [];
        const usedTargetIndices = new Set<number>();

        while (targetWallets.length < targetCount && usedTargetIndices.size < targetMnemonics.length) {
          const randomIndex = Math.floor(Math.random() * targetMnemonics.length);
          if (usedTargetIndices.has(randomIndex)) continue;

          usedTargetIndices.add(randomIndex);
          const mnemonic = targetMnemonics[randomIndex];

          const { wallet } = await initializeWalletWithRetry(() =>
            IssuerSparkWallet.initialize({
              mnemonicOrSeed: mnemonic,
              options: networkConfig,
            })
          );

          const address = await wallet.getSparkAddress();
          targetWallets.push({ wallet, address });

          if (targetWallets.length % 10 === 0) {
            console.log(`    Initialized ${targetWallets.length}/${targetCount} target wallets`);
          }
        }

        console.log(`  All ${targetWallets.length} target wallets initialized`);

        console.log(`  Starting transfers...`);
        let totalTransferred = 0;
        let successfulTransfers = 0;
        const targetTransfers: Map<string, { wallet: IssuerSparkWallet; transferCount: number }> = new Map();

        for (const { wallet: sourceWallet, address: sourceAddress, balance } of fundedSourceWallets) {
          const targetWallet = targetWallets[Math.floor(Math.random() * targetWallets.length)];

          try {
            console.log(
              `    ${sourceAddress.substring(0, 10)}... -> ${targetWallet.address.substring(0, 10)}...: ${balance} sats`
            );

            await sourceWallet.transfer({
              amountSats: Number(balance),
              receiverSparkAddress: targetWallet.address,
            });

            totalTransferred += Number(balance);
            successfulTransfers++;

            const existing = targetTransfers.get(targetWallet.address) || {
              wallet: targetWallet.wallet,
              transferCount: 0,
            };
            existing.transferCount++;
            targetTransfers.set(targetWallet.address, existing);
          } catch (error) {
            console.error(`    Transfer failed: ${error.message}`);
          }
        }

        console.log(`  Completed ${successfulTransfers} transfers, total ${totalTransferred} sats`);

        console.log(`  Querying and claiming transfers for ${targetTransfers.size} target wallets...`);

        await new Promise((resolve) => setTimeout(resolve, 2000));

        const targetPendingClaimed = 0;

        context.vars = context.vars || {};
        context.vars.transferAllFundsResult = {
          sourceWallets: sourceWallets.length,
          fundedSourceWallets: fundedSourceWallets.length,
          targetWallets: targetWallets.length,
          totalTransferred,
          successfulTransfers,
        };

        console.log(`TransferAllFunds: Complete!`);
        console.log(`  Total: ${totalTransferred} sats transferred in ${successfulTransfers} transactions`);

        ee.emit("counter", "spark.transfer_all_funds_complete", 1);
        ee.emit("histogram", "spark.transfer_all_funds_total", totalTransferred);

        callback(null, context);
      } catch (error) {
        console.error("TransferAllFunds failed:", error.message);
        ee.emit("counter", "spark.transfer_all_funds_failed", 1);
        callback(error);
      }
    };
  }

  cleanupPools(): EngineStep {
    const poolManager = this.poolManager;

    return async function (context: SparkContext, callback) {
      try {
        console.log("CleanupPools: Starting complete cleanup...");

        console.log("  Unlocking all wallets...");
        const unlockedCount = lockedWallets.size;

        for (const [name, lockedInfo] of lockedWallets) {
          try {
            const address = await lockedInfo.wallet.getSparkAddress();

            if (await removeLockFile(address)) {
              console.log(`  Removed lock file for wallet ${name} (${address})`);
            }

            const pool = walletPools.get(lockedInfo.pool);
            if (pool && !pool.available.includes(lockedInfo.wallet)) {
              pool.available.push(lockedInfo.wallet);
            }
          } catch (error) {
            console.warn(`  Failed to unlock wallet ${name}:`, error);
          }
        }

        lockedWallets.clear();
        if (context.vars) {
          for (const key in context.vars) {
            if (context.vars[key]?.wallet && context.vars[key]?.address) {
              delete context.vars[key];
            }
          }
        }

        if (context.scenarioLockedWallets) {
          context.scenarioLockedWallets = [];
        }

        console.log(`  Unlocked ${unlockedCount} wallets`);

        console.log("  Clearing all wallet pools...");
        poolManager.clearAll();

        const poolCount = walletPools.size;
        walletPools.clear();

        console.log(`  Cleared ${poolCount} pools`);

        console.log("  Cleaning up all lock files...");
        let cleanedFiles = 0;

        try {
          const { LockManager } = await import("./lock-manager");
          const lockManager = LockManager.getInstance();
          const allLocks = await lockManager.getAllLocks();

          for (const [address] of allLocks) {
            if (await lockManager.removeLock(address)) {
              cleanedFiles++;
            }
          }
        } catch (error) {
          console.warn("  Failed to clean up locks:", error);
        }

        console.log(`  Cleaned up ${cleanedFiles} lock files`);
        console.log("CleanupPools: Complete cleanup finished successfully");

        callback(null, context);
      } catch (error) {
        console.error("CleanupPools failed:", error.message);
        callback(error);
      }
    };
  }

  getStaticAddress(params: { walletName: string; storeAs: string }): EngineStep {
    const ee = this.ee;

    return async function (context: SparkContext, callback) {
      const startTime = Date.now();

      console.log(`Getting static address for wallet ${params.walletName}...`);
      try {
        const walletInfo = params.walletName ? context.vars?.[params.walletName] : context.sparkWallet;
        if (!walletInfo) {
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        if (!walletInfo) {
          console.error(`  ERROR: Wallet "${params.walletName || "default"}" not found in context`);
          console.error(`  context.vars keys:`, context.vars ? Object.keys(context.vars) : "undefined");
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        const { balance } = await safeGetBalance(walletInfo.wallet);
        console.log(`Wallet ${walletInfo.name} wallet with ${balance} sats...`);

        const wallet: IssuerSparkWallet = walletInfo.wallet;

        // Check if the wallet is already has static address
        const staticAddresses = await wallet.queryStaticDepositAddresses();
        let staticDepostAddress: string;
        if (staticAddresses && staticAddresses.length > 0) {
          console.log(`Static address already exists for wallet ${walletInfo.name}: ${staticAddresses[0]}`);
          staticDepostAddress = staticAddresses[0];
        } else {
          console.log(`Creating new static address for wallet ${walletInfo.name}...`);
          staticDepostAddress = await wallet.getStaticDepositAddress();
          console.log(`New static address created: ${staticDepostAddress}`);
        }

        context.vars = context.vars || {};
        context.vars[params.storeAs] = {
          walletName: params.walletName,
          wallet: wallet,
          staticAddress: staticDepostAddress,
          sparkAddress: await wallet.getSparkAddress(),
          balance: balance,
        };

        ee.emit("histogram", "spark.get_static_address_time", Date.now() - startTime);
        ee.emit("counter", "spark.get_static_address_success", 1);
        callback(null, context);
      } catch (error) {
        console.error(`failed to get static address for ${params.walletName}:`, error.message);
        ee.emit("counter", "spark.get_static_address_failed", 1);
        callback(error);
      }
    };
  }

  printWalletInfo(params: { storedName: string }): EngineStep {
    const ee = this.ee;
    const poolManager = this.poolManager;
    return async function (context: SparkContext, callback) {
      const startTime = Date.now();
      try {
        const walletInfo: {
          walletName: string;
          wallet: IssuerSparkWallet;
          staticAddress: string;
          sparkAddress: string;
          balance: bigint;
        } = context.vars?.[params.storedName];

        if (!walletInfo) {
          console.error(`  ERROR: Wallet "${params.storedName}" not found in context`);
          console.error(`  context.vars keys:`, context.vars ? Object.keys(context.vars) : "undefined");
          throw new Error(`Wallet ${params.storedName} not found`);
        }

        console.log(`Wallet Info for ${params.storedName}:`);
        console.log(`  Wallet Name: ${walletInfo.walletName}`);
        console.log(`  Spark Address: ${walletInfo.sparkAddress}`);
        console.log(`  Static Address: ${walletInfo.staticAddress}`);
        console.log(`  Balance: ${walletInfo.balance} sats`);

        ee.emit("histogram", "spark.print_wallet_info_time", Date.now() - startTime);
        ee.emit("counter", "spark.print_wallet_info_success", 1);
        callback(null, context);
      } catch (error) {
        console.error(`failed to print wallet info from store with name ${params.storedName}:`, error.message);
        ee.emit("counter", "spark.print_wallet_info_failed", 1);
        callback(error);
      }
    };
  }

  claimStaticDeposit(params: { walletName: string }): EngineStep {
    const ee = this.ee;
    return async function (context: SparkContext, callback) {
      const startTime = Date.now();

      try {
        const walletInfo: {
          wallet: IssuerSparkWallet;
          txId: string;
        } = context.vars?.[params.walletName];

        if (!walletInfo) {
          console.error(`  ERROR: Wallet "${params.walletName}" not found in context`);
          console.error(`  context.vars keys:`, context.vars ? Object.keys(context.vars) : "undefined");
          throw new Error(`Wallet ${params.walletName} not found`);
        }

        const quote = await walletInfo.wallet.getClaimStaticDepositQuote(walletInfo.txId);
        if (!quote) {
          console.error(`  ERROR: No quote found for static deposit claim in wallet ${params.walletName}`);
          throw new Error(`No quote found for static deposit claim in wallet ${params.walletName}`);
        }

        await new Promise((resolve) => setTimeout(resolve, 30000));

        console.log(`  Quote for static deposit claim: ${JSON.stringify(quote)}`);
        console.log(`  Claiming static deposit for wallet ${params.walletName}...`);
        const q = await walletInfo.wallet.claimStaticDeposit({
          transactionId: walletInfo.txId,
          creditAmountSats: quote.creditAmountSats,
          sspSignature: quote.signature,
        });

        await new Promise((resolve) => setTimeout(resolve, 60000)); // Wait for 60 seconds to ensure claim is processed

        let pendingTransfer = await (walletInfo.wallet as any).transferService.queryTransfer(q.transferId);

        console.log(` Transfer status: ${pendingTransfer.status}\n\n`);

        if (!pendingTransfer) {
          console.log(` Transfer not found (ID: ${q.transferId})`);
          throw new Error(`Transfer not found (ID: ${q.transferId})`);
        }

        await (walletInfo.wallet as any).claimTransfer({
          transfer: pendingTransfer,
          optimize: true,
        });

        await new Promise((resolve) => setTimeout(resolve, 1000));
        const { balance: balanceAfterClaim } = await walletInfo.wallet.getBalance();

        console.log(`\n\n New Balance: ${balanceAfterClaim}\n\n`);
        console.log(`  Static deposit claimed successfully for wallet ${params.walletName}`);
        ee.emit("histogram", "spark.claim_static_deposit_time", Date.now() - startTime);
        ee.emit("counter", "spark.claim_static_deposit_success", 1);
        callback(null, context);
      } catch (error) {
        console.error(`failed to claim static depost ${params.walletName}:`, error.message);
        ee.emit("counter", "spark.claim_static_deposit_failed", 1);
        callback(error);
      }
    };
  }

  withdraw(params?: { senderWallet: string; receiverWallet: string; storeAs: string; amount: number }): EngineStep {
    const ee = this.ee;

    return async function (context, callback) {
      try {
        console.log(`Starting withdrawal from wallet: ${params.senderWallet}`);

        // get sender wallet instance
        let namedWalletInfo = context.vars?.[params.senderWallet];
        if (!namedWalletInfo) {
          throw new Error(`Wallet "${params.senderWallet}" not found. Make sure it's locked first.`);
        }

        const sendWallet: IssuerSparkWallet = namedWalletInfo.wallet;

        const { balance } = await safeGetBalance(sendWallet);
        console.log(`${params.senderWallet} balance is ${balance.toString()} sats...`);
        await new Promise((resolve) => setTimeout(resolve, 2000));

        if (balance.valueOf() <= params.amount) {
          throw new Error(
            `${params.senderWallet} balance is insuficcient: ${balance.toString()} sats, withrawal amount: ${params.amount} sats...`
          );
        }
        namedWalletInfo = context.vars?.[params.receiverWallet];
        if (!namedWalletInfo) {
          throw new Error(`Wallet "${params.receiverWallet}" not found. Make sure it's locked first.`);
        }
        const receiverWallet: IssuerSparkWallet = namedWalletInfo.wallet;
        const staticReceiverAddress = namedWalletInfo.staticAddress;

        // get withdrawal fee
        const feeQuote = await sendWallet.getWithdrawalFeeQuote({
          amountSats: params.amount,
          withdrawalAddress: staticReceiverAddress,
        });

        await new Promise((resolve) => setTimeout(resolve, 2000));

        const userFee = feeQuote.userFeeSlow.originalValue + feeQuote.l1BroadcastFeeSlow.originalValue;
        console.log(`Fee quote for withdrawal is: ${userFee} sats...`);

        if (userFee >= params.amount) {
          throw new Error("estimated fee exceeds wallet balance");
        }

        console.log(`Withdrawal amount is ${params.amount} sats...`);

        const result = await sendWallet.withdraw({
          amountSats: params.amount,
          onchainAddress: staticReceiverAddress,
          feeQuote: feeQuote!,
          exitSpeed: "SLOW" as any,
          deductFeeFromWithdrawalAmount: true,
        });

        await new Promise((resolve) => setTimeout(resolve, 40000)); // Wait for 40 seconds to ensure claim is processed

        context.vars = context.vars || {};
        context.vars[params.storeAs] = {
          wallet: receiverWallet,
          txId: result.coopExitTxid,
        };

        ee.emit("counter", "spark.exit_succesful", 1);
        ee.emit("counter", "spark.amount_withdrawed", Number(params.amount));

        callback(null, context);
      } catch (error) {
        console.error("Exit Spark failed: ", error);
        ee.emit("counter", "spark.exit_error", 1);
        callback(error);
      }
    };
  }
}
