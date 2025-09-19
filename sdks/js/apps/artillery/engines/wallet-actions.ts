import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { IssuerSparkWalletNoEvents } from "./issuer-wallet-no-events";
import { getLoadtestNetworkConfig } from "./network-config";
import { WalletPoolManager } from "./wallet-pool-manager";
import {
  beforeScenario as lockWalletsHook,
  lockedWallets,
  createLockFile as createLockFileFromHooks,
  removeLockFile as removeLockFileFromHooks,
  isLocked as isLockedFromHooks,
  walletPools,
} from "./hooks";
import type { SparkContext, ArtilleryEventEmitter, EngineStep } from "./types";
import { randomUUID } from "crypto";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import * as path from "path";
import { ConnectionManager, KeyDerivationType } from "@buildonspark/spark-sdk";

type WalletType = IssuerSparkWallet | IssuerSparkWalletNoEvents;

const createLockFile = createLockFileFromHooks;
const removeLockFile = removeLockFileFromHooks;
const isLocked = isLockedFromHooks;

export class WalletActions {
  private poolManager: WalletPoolManager;
  private engine: any; // Reference to parent engine

  constructor(
    private ee: ArtilleryEventEmitter,
    engine?: any,
  ) {
    this.poolManager = WalletPoolManager.getInstance();
    this.engine = engine;
  }

  private getWallet(context: SparkContext, walletName?: string) {
    const walletInfo = walletName
      ? context.vars?.[walletName]
      : context.sparkWallet;
    if (!walletInfo?.wallet)
      throw new Error(`Wallet ${walletName || "default"} not found`);
    return walletInfo;
  }

  private createStep(fn: (context: SparkContext) => Promise<void>): EngineStep {
    return async (context: SparkContext, callback) => {
      try {
        await fn(context);
        callback(null, context);
      } catch (error) {
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
    return fileContent.split("\n").filter((line) => line.trim() !== "");
  }

  initNamedWallet(
    params: { name: string; mnemonic?: string } = { name: "" },
  ): EngineStep {
    return this.createStep(async (context) => {
      if (!params.name) {
        throw new Error("Wallet name is required");
      }

      if (this.poolManager.getNamedWallet(params.name)) {
        return;
      }

      const mnemonic = params.mnemonic || generateMnemonic(wordlist, 256);
      const { wallet } = await IssuerSparkWallet.initialize({
        mnemonicOrSeed: mnemonic,
        options: getLoadtestNetworkConfig(),
      });

      await this.poolManager.registerNamedWallet(params.name, wallet, mnemonic);

      this.ee.emit("histogram", "spark.named_wallet_init_time", Date.now());
    });
  }

  fundWallet(params: { walletName?: string; amount: number }): EngineStep {
    return this.createStep(async (context) => {
      const walletInfo = this.getWallet(context, params.walletName);
      const balance = await walletInfo.wallet.getBalance();

      if (params.walletName && context.vars?.[params.walletName]) {
        context.vars[params.walletName].balance = balance.balance;
      }
    });
  }

  lockWallets(params?: any): EngineStep {
    return async function (context: SparkContext, callback) {
      try {
        const vuId =
          (context as any)._uid || context.vars?.$uuid || randomUUID();

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
        context._scenarioSpec.lockWallets =
          modifiedParams || context._scenarioSpec?.lockWallets;

        await new Promise((resolve, reject) => {
          lockWalletsHook(
            context,
            {} as ArtilleryEventEmitter,
            (error: any) => {
              if (error) reject(error);
              else resolve(undefined);
            },
          );
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
        const walletsToUnlock =
          params?.wallets || context.scenarioLockedWallets || [];

        for (const walletName of walletsToUnlock) {
          const namedWallet = context.vars?.[walletName];
          const lockedInfo = lockedWallets.get(walletName);

          if (lockedInfo) {
            const address = await lockedInfo.wallet.getSparkAddress();

            if (await removeLockFile(address)) {
            }

            const pool = walletPools.get(lockedInfo.pool);
            if (pool) {
              pool.available.push(lockedInfo.wallet);
              await poolManager.unlockWallet(lockedInfo.wallet);
            }

            lockedWallets.delete(walletName);
          }

          if (context.vars?.[walletName]) {
            delete context.vars[walletName];
          }
        }

        if (context.scenarioLockedWallets) {
          context.scenarioLockedWallets = context.scenarioLockedWallets.filter(
            (name) => !walletsToUnlock.includes(name),
          );
        }

        callback(null, context);
      } catch (error) {
        callback(error);
      }
    };
  }

  getBalance(params?: { walletName?: string; storeAs?: string }): EngineStep {
    return this.createStep(async (context) => {
      const walletInfo = this.getWallet(context, params?.walletName);
      const { balance } = await walletInfo.wallet.getBalance();

      if (params?.storeAs) {
        context.vars = context.vars || {};
        context.vars[params.storeAs] = balance;
      }
    });
  }

  getIdentityPublicKey(params?: {
    walletName?: string;
    storeAs?: string;
  }): EngineStep {
    return this.createStep(async (context) => {
      const walletInfo = this.getWallet(context, params?.walletName);
      const publicKeyHex = await walletInfo.wallet.getIdentityPublicKey();

      if (params?.storeAs) {
        context.vars = context.vars || {};
        context.vars[params.storeAs] = publicKeyHex;
      }
    });
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
        const walletConfigs = params?.wallets || [];
        if (walletConfigs.length === 0) {
          callback(null, context);
          return;
        }

        const selectedIndices: Map<string, Set<number>> = new Map();

        const vuId = context.vars?.__vuId || `vu-${Date.now()}`;

        for (const walletConfig of walletConfigs) {
          const {
            walletName,
            pool: poolName = "transfer-pool",
            lock = false,
            minBalance = 0,
          } = walletConfig;

          const pool = walletPools.get(poolName);
          if (!pool || pool.wallets.length === 0) {
            throw new Error(`Pool "${poolName}" not found or empty`);
          }

          if (!selectedIndices.has(poolName)) {
            selectedIndices.set(poolName, new Set());
          }

          const poolSelectedIndices = selectedIndices.get(poolName)!;

          const { isLocked } = await import("./hooks");

          const availableWallets: {
            wallet: IssuerSparkWallet;
            index: number;
            address: string;
            balance: bigint;
          }[] = [];

          for (let i = 0; i < pool.wallets.length; i++) {
            if (!poolSelectedIndices.has(i)) {
              const wallet = pool.wallets[i];
              const address = await wallet.getSparkAddress();

              if (!(await isLocked(address))) {
                if (minBalance > 0) {
                  try {
                    const balanceInfo = await wallet.getBalance();
                    const balance = balanceInfo.balance || 0n;

                    if (balance >= BigInt(minBalance)) {
                      availableWallets.push({
                        wallet,
                        index: i,
                        address,
                        balance,
                      });
                    } else {
                      throw new Error(
                        `Wallet balance ${balance} is less than required minimum ${minBalance}`,
                      );
                    }
                  } catch (error) {
                    throw error;
                  }
                } else {
                  availableWallets.push({
                    wallet,
                    index: i,
                    address,
                    balance: 0n,
                  });
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
                  const balanceInfo = await wallet.getBalance();
                  const balance = balanceInfo.balance || 0n;
                  if (balance < BigInt(minBalance)) {
                    insufficientBalanceCount++;
                  }
                } catch (error) {
                  throw error;
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

          const randomIndex = Math.floor(
            Math.random() * availableWallets.length,
          );
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
              context.scenarioLockedWallets =
                context.scenarioLockedWallets || [];
              context.scenarioLockedWallets.push(walletName);

              lockedWallets.set(walletName, {
                wallet: selectedWallet,
                pool: poolName,
              });
            } else {
              throw new Error(
                `Failed to lock wallet "${walletName}" - it may have been locked by another process`,
              );
            }
          } else {
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

        callback(null, context);
      } catch (error) {
        callback(error);
      }
    };
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
  ): Promise<{
    wallet: WalletType;
    index: number;
    address: string;
    balance: bigint;
    publicKey: Uint8Array;
  } | null> {
    if (mnemonics.length === 0) {
      return null;
    }

    if (startIndex >= mnemonics.length) {
      startIndex = 0;
    }
    const { tryLockOneOf, removeLockFile } = await import("./hooks");
    const BATCH_SIZE = 3;

    let searchedIndices = 0;
    let currentStart = startIndex;
    let wrapped = false;
    const maxSearchAttempts = Math.ceil(mnemonics.length / BATCH_SIZE) * 2;
    let searchAttempts = 0;

    while (
      searchedIndices < mnemonics.length &&
      searchAttempts < maxSearchAttempts
    ) {
      searchAttempts++;

      if (currentStart >= mnemonics.length) {
        if (wrapped) {
          break;
        }
        currentStart = 0;
        wrapped = true;
      }

      const maxEnd = wrapped ? startIndex : mnemonics.length;
      const batchEnd = Math.min(currentStart + BATCH_SIZE, maxEnd);

      if (currentStart >= batchEnd || (wrapped && currentStart >= startIndex)) {
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
          [batchIndices[i], batchIndices[j]] = [
            batchIndices[j],
            batchIndices[i],
          ];
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
          await removeLockFile(lockedKey);
          continue;
        }

        try {
          const networkConfig = getLoadtestNetworkConfig();

          const { wallet } = await IssuerSparkWalletNoEvents.initialize({
            options: networkConfig,
            mnemonicOrSeed: mnemonic,
          });

          const address = await wallet.getSparkAddress();
          const publicKeyHex = await wallet.getIdentityPublicKey();
          const publicKey = hexToBytes(publicKeyHex);

          if (minBalance !== undefined && minBalance > 0) {
            const { balance } = await wallet.getBalance();

            if (balance < minBalance) {
              await removeLockFile(lockedKey);
              continue;
            }

            return {
              wallet,
              index: lockedIndex,
              address,
              balance,
              publicKey,
            };
          } else {
            return {
              wallet,
              index: lockedIndex,
              address,
              balance: 0n,
              publicKey,
            };
          }
        } catch (error) {
          await removeLockFile(lockedKey);
          continue;
        }
      }

      currentStart = batchEnd;
    }

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
        const network = (process.env.SPARK_NETWORK || "LOCAL") as
          | "MAINNET"
          | "REGTEST"
          | "TESTNET"
          | "SIGNET"
          | "LOCAL";

        const isLoadtestEnvironment = true;
        const effectiveNetwork =
          network === "LOCAL" && isLoadtestEnvironment ? "REGTEST" : network;

        const poolConfigs = params?.pools || [];

        if (poolConfigs.length === 0) {
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
            const existingPool = walletPools.get(poolName);
            if (!existingPool) {
              return { poolName, successfulWallets: 0, totalPoolsCreated: 0 };
            }

            if (namedWallets.length > 0) {
              for (const namedWallet of namedWallets) {
                const lockedWallet = lockedWallets.get(namedWallet.name);
                if (lockedWallet && lockedWallet.pool === poolName) {
                  const address = await lockedWallet.wallet.getSparkAddress();
                  const publicKey =
                    await lockedWallet.wallet.getIdentityPublicKey();
                  const balance = await lockedWallet.wallet.getBalance();

                  context.vars = context.vars || {};
                  context.vars[namedWallet.name] = {
                    wallet: lockedWallet.wallet,
                    name: namedWallet.name,
                    address: address,
                    publicKey:
                      typeof publicKey === "string"
                        ? publicKey
                        : bytesToHex(publicKey),
                    balance: balance,
                  };
                } else {
                  let mnemonics: string[] = [];
                  if (poolConfig.mnemonicsFile) {
                    try {
                      mnemonics = await self.loadMnemonicsFromFile(
                        poolConfig.mnemonicsFile,
                      );
                    } catch (error) {
                      throw error;
                    }
                  }

                  const network = (process.env.SPARK_NETWORK || "LOCAL") as
                    | "MAINNET"
                    | "REGTEST"
                    | "TESTNET"
                    | "SIGNET"
                    | "LOCAL";
                  const effectiveNetwork =
                    network === "LOCAL" ? "REGTEST" : network;

                  const startIndex = Math.floor(
                    Math.random() * mnemonics.length,
                  );
                  const walletInfo = await self.findAvailableWallet(
                    mnemonics,
                    startIndex,
                    effectiveNetwork,
                    poolName,
                    namedWallet.minBalance || poolConfig.minBalance,
                    namedWallet,
                    undefined,
                    true,
                  );

                  if (walletInfo) {
                    existingPool.wallets.push(walletInfo.wallet);

                    const lockMetadata = {
                      walletName: namedWallet.name,
                      poolName,
                      scenarioId: vuId,
                      lockedBy: "initializePools",
                      timestamp: new Date().toISOString(),
                    };

                    if (
                      await createLockFile(walletInfo.address, lockMetadata)
                    ) {
                      lockedWallets.set(namedWallet.name, {
                        wallet: walletInfo.wallet,
                        pool: poolName,
                      });

                      context.scenarioLockedWallets =
                        context.scenarioLockedWallets || [];
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
                  } else {
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
          }

          let targetAmount: number;
          if (poolConfig.amountEnvName) {
            const envValue = process.env[poolConfig.amountEnvName];
            if (!envValue) {
              throw new Error(
                `Environment variable ${poolConfig.amountEnvName} not found for pool "${poolName}"`,
              );
            }
            targetAmount = parseInt(envValue, 10);
            if (isNaN(targetAmount) || targetAmount <= 0) {
              throw new Error(
                `Invalid pool size from env var ${poolConfig.amountEnvName}: ${envValue}`,
              );
            }
          } else if (poolConfig.amount !== undefined) {
            targetAmount = poolConfig.amount;
          } else if (namedWallets.length > 0) {
            targetAmount = namedWallets.length;
          } else {
            targetAmount = parseInt(
              process.env.SPARK_DEFAULT_POOL_SIZE || "50",
            );
          }

          const wallets: IssuerSparkWallet[] = [];
          const available: IssuerSparkWallet[] = [];

          let mnemonics: string[] = [];
          if (poolConfig.mnemonicsFile) {
            try {
              mnemonics = await self.loadMnemonicsFromFile(
                poolConfig.mnemonicsFile,
              );
            } catch (error) {
              throw error;
            }
          } else {
          }

          let mnemonicIndex = 0;
          let successfulWallets = 0;
          let lockedCount = 0;

          const batchSize = poolConfig.batchSize || 5;

          if (namedWallets.length > 0) {
            for (
              let i = 0;
              i < namedWallets.length && successfulWallets < targetAmount;
              i += batchSize
            ) {
              const batch = namedWallets.slice(
                i,
                Math.min(i + batchSize, namedWallets.length),
              );

              const batchPromises = batch.map(async (namedWallet) => {
                if (successfulWallets >= targetAmount) return null;

                const walletMinBalance = namedWallet.minBalance || minBalance;

                try {
                  const walletInfo = await self.findAvailableWallet(
                    mnemonics,
                    mnemonicIndex,
                    effectiveNetwork,
                    poolName,
                    namedWallet.minBalance || minBalance,
                    namedWallet,
                    walletStateDir,
                    poolConfig.randomSelection !== false,
                  );

                  if (!walletInfo) {
                    const requiredBalance =
                      namedWallet.minBalance || minBalance;
                    throw new Error(
                      `Could not find available wallet for "${namedWallet.name}" in pool "${poolName}"${requiredBalance ? ` with minimum balance ${requiredBalance} sats` : ""}`,
                    );
                  }

                  return { walletInfo, namedWallet };
                } catch (error) {
                  throw error;
                }
              });

              const batchResults = await Promise.all(batchPromises);

              for (const result of batchResults) {
                if (!result) {
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

                    lockedWallets.set(namedWallet.name, {
                      wallet: walletInfo.wallet,
                      pool: poolName,
                    });

                    context.scenarioLockedWallets =
                      context.scenarioLockedWallets || [];
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
              }
            }
          }

          while (
            successfulWallets < targetAmount &&
            mnemonicIndex < mnemonics.length
          ) {
            const remainingNeeded = targetAmount - successfulWallets;
            const currentBatchSize = Math.min(batchSize, remainingNeeded);

            const batchPromises = [];
            for (
              let i = 0;
              i < currentBatchSize && mnemonicIndex < mnemonics.length;
              i++
            ) {
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
                  )
                  .then((walletInfo) => ({
                    walletInfo,
                    attemptedIndex: currentIndex,
                  }))
                  .catch((error) => {
                    return { walletInfo: null, attemptedIndex: currentIndex };
                  }),
              );
            }

            const batchResults = await Promise.all(batchPromises);

            const attemptedIndices = batchResults
              .filter((r) => r !== null)
              .map((r) => r.attemptedIndex);

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

                  lockedWallets.set(walletName, {
                    wallet: walletInfo.wallet,
                    pool: poolName,
                  });

                  context.scenarioLockedWallets =
                    context.scenarioLockedWallets || [];
                  context.scenarioLockedWallets.push(walletName);
                }
              } else {
                available.push(walletInfo.wallet);
              }
            }

            if (batchSuccessCount === 0) {
              break;
            }

            if (
              successfulWallets < targetAmount &&
              mnemonicIndex < mnemonics.length
            ) {
              await new Promise((resolve) => setTimeout(resolve, 100));
            }
          }

          walletPools.set(poolName, { wallets, available });

          return { poolName, successfulWallets, totalPoolsCreated: 1 };
        });

        const poolResults = await Promise.all(poolInitPromises);

        poolResults.forEach((result) => {
          totalWalletsInitialized += result.successfulWallets;
          totalPoolsCreated += result.totalPoolsCreated;
        });

        const initTime = Date.now() - startTime;

        let totalWalletsInPools = 0;
        for (const pool of walletPools.values()) {
          if (pool && pool.wallets) {
            totalWalletsInPools += pool.wallets.length;
          }
        }

        const scenarioEE = self.engine?.scenarioEE;
        if (scenarioEE) {
          scenarioEE.emit("histogram", "spark.initialize_pools_time", initTime);
          scenarioEE.emit("counter", "spark.pools_created", totalPoolsCreated);
          scenarioEE.emit(
            "counter",
            "spark.wallets_initialized",
            totalWalletsInitialized,
          );
          scenarioEE.emit("counter", "spark.initialize_pools_success", 1);
        } else {
          self.ee.emit("histogram", "spark.initialize_pools_time", initTime);
          self.ee.emit("counter", "spark.pools_created", totalPoolsCreated);
          self.ee.emit(
            "counter",
            "spark.wallets_initialized",
            totalWalletsInitialized,
          );
          self.ee.emit("counter", "spark.initialize_pools_success", 1);
        }

        callback(null, context);
      } catch (error) {
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

  setTransferAmount(params?: {
    amount?: number;
    min?: number;
    max?: number;
    storeAs?: string;
  }): EngineStep {
    return this.createStep(async (context) => {
      let amount: number;

      if (params?.amount !== undefined) {
        amount = params.amount;
      } else if (params?.min !== undefined && params?.max !== undefined) {
        amount =
          Math.floor(Math.random() * (params.max - params.min + 1)) +
          params.min;
      } else {
        amount = Math.floor(Math.random() * 99001) + 1000;
      }

      context.vars = context.vars || {};
      const key = params?.storeAs || "transferAmount";
      context.vars[key] = amount;
    });
  }

  collectFundsToPool(params?: {
    targetPool: string;
    sourcePools?: string[];
    leaveAmount?: number;
  }): EngineStep {
    return async function (context: SparkContext, callback) {
      try {
        if (!params?.targetPool) {
          throw new Error("targetPool is required for collectFundsToPool");
        }

        const targetPool = walletPools.get(params.targetPool);
        if (!targetPool || targetPool.wallets.length === 0) {
          throw new Error(
            `Target pool "${params.targetPool}" not found or empty`,
          );
        }

        let sourcePools: string[];
        if (params.sourcePools && params.sourcePools.length > 0) {
          sourcePools = params.sourcePools;
        } else {
          sourcePools = Array.from(walletPools.keys()).filter(
            (name) => name !== params.targetPool,
          );
        }

        const leaveAmount = params.leaveAmount || 0;

        if (targetPool.available.length === 0) {
          throw new Error(
            `No available wallets in target pool "${params.targetPool}"`,
          );
        }

        const collectorWallet = targetPool.available[0];
        const collectorAddress = await collectorWallet.getSparkAddress();

        let totalCollected = 0;
        let walletsProcessed = 0;

        for (const poolName of sourcePools) {
          const pool = walletPools.get(poolName);
          if (!pool) {
            continue;
          }

          for (const wallet of pool.wallets) {
            try {
              const walletAddress = await wallet.getSparkAddress();
              if (walletAddress === collectorAddress) {
                continue;
              }

              const balanceInfo = await wallet.getBalance();
              const balance = balanceInfo.balance || 0n;

              const transferAmount =
                balance > BigInt(leaveAmount)
                  ? balance - BigInt(leaveAmount)
                  : 0n;

              if (transferAmount > 0) {
                try {
                  const transferResult = await wallet.transfer({
                    amountSats: Number(transferAmount),
                    receiverSparkAddress: collectorAddress,
                  });

                  totalCollected += Number(transferAmount);
                  walletsProcessed++;
                } catch (error) {
                  throw error;
                }
              } else {
              }
            } catch (error) {
              throw error;
            }
          }
        }

        const finalBalance = await collectorWallet.getBalance();

        context.vars = context.vars || {};
        context.vars.totalCollected = totalCollected;
        context.vars.walletsProcessed = walletsProcessed;

        this.ee.emit("counter", "spark.funds_collected", totalCollected);
        this.ee.emit(
          "counter",
          "spark.wallets_collected_from",
          walletsProcessed,
        );

        callback(null, context);
      } catch (error) {
        this.ee.emit("counter", "spark.fund_collection_failed", 1);
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
    return async function (context: SparkContext, callback) {
      try {
        if (!params?.sourcePool || !params?.targetPool) {
          throw new Error(
            "Both sourcePool and targetPool are required for transferPoolFunds",
          );
        }

        const sourcePool = walletPools.get(params.sourcePool);
        const targetPool = walletPools.get(params.targetPool);

        if (!sourcePool || sourcePool.wallets.length === 0) {
          throw new Error(
            `Source pool "${params.sourcePool}" not found or empty`,
          );
        }

        if (!targetPool || targetPool.wallets.length === 0) {
          throw new Error(
            `Target pool "${params.targetPool}" not found or empty`,
          );
        }

        const leaveAmount = params.leaveAmount || 0;
        const distributeEvenly = params.distributeEvenly || false;

        let totalAvailable = 0n;
        const sourceBalances: Map<
          string,
          { wallet: IssuerSparkWallet; balance: bigint }
        > = new Map();

        for (const wallet of sourcePool.wallets) {
          try {
            const address = await wallet.getSparkAddress();
            const balanceInfo = await wallet.getBalance();
            const balance = balanceInfo.balance || 0n;
            const transferable =
              balance > BigInt(leaveAmount)
                ? balance - BigInt(leaveAmount)
                : 0n;

            if (transferable > 0) {
              sourceBalances.set(address, { wallet, balance: transferable });
              totalAvailable += transferable;
            }
          } catch (error) {
            throw error;
          }
        }

        if (totalAvailable === 0n) {
          callback(null, context);
          return;
        }

        const targetWallets: Array<{
          wallet: IssuerSparkWallet;
          address: string;
        }> = [];
        for (const wallet of targetPool.wallets) {
          const address = await wallet.getSparkAddress();
          targetWallets.push({ wallet, address });
        }

        let totalTransferred = 0;
        let transferCount = 0;

        if (distributeEvenly) {
          const amountPerWallet = totalAvailable / BigInt(targetWallets.length);
          const remainder = totalAvailable % BigInt(targetWallets.length);

          const transfers: Array<{
            from: IssuerSparkWallet;
            to: string;
            amount: bigint;
          }> = [];
          let targetIndex = 0;
          let remainingAmount = totalAvailable;

          for (const [, { wallet: sourceWallet, balance }] of sourceBalances) {
            let walletRemaining = balance;

            while (walletRemaining > 0n && targetIndex < targetWallets.length) {
              const targetWallet = targetWallets[targetIndex];
              const targetAmount =
                targetIndex < Number(remainder)
                  ? amountPerWallet + 1n
                  : amountPerWallet;
              const transferAmount =
                walletRemaining < targetAmount ? walletRemaining : targetAmount;

              transfers.push({
                from: sourceWallet,
                to: targetWallet.address,
                amount: transferAmount,
              });

              walletRemaining -= transferAmount;
              remainingAmount -= transferAmount;

              if (
                remainingAmount <=
                BigInt(targetWallets.length - targetIndex - 1) * amountPerWallet
              ) {
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
            } catch (error) {
              throw error;
            }
          }
        } else {
          const collectorWallet = targetWallets[0];

          for (const [
            address,
            { wallet: sourceWallet, balance },
          ] of sourceBalances) {
            if (balance > 0) {
              try {
                const result = await sourceWallet.transfer({
                  amountSats: Number(balance),
                  receiverSparkAddress: collectorWallet.address,
                });

                totalTransferred += Number(balance);
                transferCount++;
              } catch (error) {
                throw error;
              }
            }
          }
        }

        context.vars = context.vars || {};
        context.vars.poolTransferTotal = totalTransferred;
        context.vars.poolTransferCount = transferCount;

        this.ee.emit(
          "counter",
          "spark.pool_funds_transferred",
          totalTransferred,
        );
        this.ee.emit(
          "counter",
          "spark.pool_transfers_completed",
          transferCount,
        );

        callback(null, context);
      } catch (error) {
        this.ee.emit("counter", "spark.pool_transfer_failed", 1);
        callback(error);
      }
    };
  }

  unlockPoolWallets(params?: {
    pools?: string[];
    force?: boolean;
  }): EngineStep {
    return async (context: SparkContext, callback) => {
      try {
        const poolsToUnlock = params?.pools || Array.from(walletPools.keys());
        const forceUnlock = params?.force || false;

        let unlockedCount = 0;
        let failedCount = 0;

        for (const poolName of poolsToUnlock) {
          const pool = walletPools.get(poolName);
          if (!pool) {
            continue;
          }

          for (const wallet of pool.wallets) {
            try {
              const address = await wallet.getSparkAddress();

              if (await isLocked(address)) {
                if (!forceUnlock) {
                  continue;
                }

                if (await removeLockFile(address)) {
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
                  failedCount++;
                }
              }
            } catch (error) {
              failedCount++;
            }
          }
        }

        this.ee.emit("counter", "spark.wallets_unlocked", unlockedCount);
        if (failedCount > 0) {
          this.ee.emit("counter", "spark.wallets_unlock_failed", failedCount);
        }

        callback(null, context);
      } catch (error) {
        this.ee.emit("counter", "spark.unlock_pool_failed", 1);
        callback(error);
      }
    };
  }

  transferAllFunds(params?: {
    sourceMnemonicsFile: string;
    targetMnemonicsFile: string;
    leaveAmount?: number;
  }): EngineStep {
    const self = this;

    return async function (context: SparkContext, callback) {
      try {
        if (!params?.sourceMnemonicsFile || !params?.targetMnemonicsFile) {
          throw new Error(
            "Both sourceMnemonicsFile and targetMnemonicsFile are required",
          );
        }

        const leaveAmount = params.leaveAmount || 0;

        const sourceMnemonics = await self.loadMnemonicsFromFile(
          params.sourceMnemonicsFile,
        );

        const targetMnemonics = await self.loadMnemonicsFromFile(
          params.targetMnemonicsFile,
        );

        const networkConfig = getLoadtestNetworkConfig();

        const sourceWallets: {
          wallet: IssuerSparkWallet;
          mnemonic: string;
          address: string;
        }[] = [];
        const sourceInitStartTime = Date.now();
        const BATCH_SIZE = 50;

        for (
          let batchStart = 0;
          batchStart < sourceMnemonics.length;
          batchStart += BATCH_SIZE
        ) {
          const batchEnd = Math.min(
            batchStart + BATCH_SIZE,
            sourceMnemonics.length,
          );
          const batchNumber = Math.floor(batchStart / BATCH_SIZE) + 1;
          const totalBatches = Math.ceil(sourceMnemonics.length / BATCH_SIZE);

          const batchStartTime = Date.now();

          const batchPromises = [];
          for (let i = batchStart; i < batchEnd; i++) {
            const mnemonic = sourceMnemonics[i];
            const walletIndex = i + 1;

            batchPromises.push(
              (async () => {
                try {
                  const { wallet } = await IssuerSparkWallet.initialize({
                    mnemonicOrSeed: mnemonic,
                    options: networkConfig,
                  });

                  const address = await wallet.getSparkAddress();
                  return { wallet, mnemonic, address, index: walletIndex };
                } catch (error) {
                  throw error;
                }
              })(),
            );
          }

          const batchResults = await Promise.all(batchPromises);

          for (const result of batchResults) {
            if (result) {
              sourceWallets.push({
                wallet: result.wallet,
                mnemonic: result.mnemonic,
                address: result.address,
              });
            }
          }

          const batchEndTime = Date.now();
          const batchTime = batchEndTime - batchStartTime;
          const avgTimePerWallet = Math.floor(
            batchTime / (batchEnd - batchStart),
          );
        }

        const totalInitTime = Date.now() - sourceInitStartTime;

        let totalAvailable = 0n;
        const fundedSourceWallets: {
          wallet: IssuerSparkWallet;
          address: string;
          balance: bigint;
        }[] = [];
        const balanceCheckStartTime = Date.now();

        for (
          let batchStart = 0;
          batchStart < sourceWallets.length;
          batchStart += BATCH_SIZE
        ) {
          const batchEnd = Math.min(
            batchStart + BATCH_SIZE,
            sourceWallets.length,
          );
          const batchNumber = Math.floor(batchStart / BATCH_SIZE) + 1;
          const totalBatches = Math.ceil(sourceWallets.length / BATCH_SIZE);

          const batchPromises = sourceWallets
            .slice(batchStart, batchEnd)
            .map(async ({ wallet, address }) => {
              try {
                const { balance } = await wallet.getBalance();
                const transferable =
                  balance > BigInt(leaveAmount)
                    ? balance - BigInt(leaveAmount)
                    : 0n;

                if (transferable > 0) {
                  return { wallet, address, balance: transferable };
                }
                return null;
              } catch (error) {
                throw error;
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
        }

        const balanceCheckTime = Date.now() - balanceCheckStartTime;

        if (totalAvailable === 0n) {
          callback(null, context);
          return;
        }

        const targetCount = Math.min(
          targetMnemonics.length,
          fundedSourceWallets.length,
        );

        const targetWallets: { wallet: IssuerSparkWallet; address: string }[] =
          [];
        const usedTargetIndices = new Set<number>();

        while (
          targetWallets.length < targetCount &&
          usedTargetIndices.size < targetMnemonics.length
        ) {
          const randomIndex = Math.floor(
            Math.random() * targetMnemonics.length,
          );
          if (usedTargetIndices.has(randomIndex)) continue;

          usedTargetIndices.add(randomIndex);
          const mnemonic = targetMnemonics[randomIndex];

          const { wallet } = await IssuerSparkWallet.initialize({
            mnemonicOrSeed: mnemonic,
            options: networkConfig,
          });

          const address = await wallet.getSparkAddress();
          targetWallets.push({ wallet, address });
        }

        let totalTransferred = 0;
        let successfulTransfers = 0;
        const targetTransfers: Map<
          string,
          { wallet: IssuerSparkWallet; transferCount: number }
        > = new Map();

        for (const {
          wallet: sourceWallet,
          address: sourceAddress,
          balance,
        } of fundedSourceWallets) {
          const targetWallet =
            targetWallets[Math.floor(Math.random() * targetWallets.length)];

          try {
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
            throw error;
          }
        }

        await new Promise((resolve) => setTimeout(resolve, 2000));

        context.vars = context.vars || {};
        context.vars.transferAllFundsResult = {
          sourceWallets: sourceWallets.length,
          fundedSourceWallets: fundedSourceWallets.length,
          targetWallets: targetWallets.length,
          totalTransferred,
          successfulTransfers,
        };

        this.ee.emit("counter", "spark.transfer_all_funds_complete", 1);
        this.ee.emit(
          "histogram",
          "spark.transfer_all_funds_total",
          totalTransferred,
        );

        callback(null, context);
      } catch (error) {
        this.ee.emit("counter", "spark.transfer_all_funds_failed", 1);
        callback(error);
      }
    };
  }

  cleanupPools(): EngineStep {
    const poolManager = this.poolManager;
    return async function (context: SparkContext, callback) {
      try {
        const unlockedCount = lockedWallets.size;

        for (const [name, lockedInfo] of lockedWallets) {
          try {
            const address = await lockedInfo.wallet.getSparkAddress();

            if (await removeLockFile(address)) {
            }

            const pool = walletPools.get(lockedInfo.pool);
            if (pool && !pool.available.includes(lockedInfo.wallet)) {
              pool.available.push(lockedInfo.wallet);
            }
          } catch (error) {}
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

        poolManager.clearAll();

        const poolCount = walletPools.size;
        walletPools.clear();

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
        } catch (error) {}

        callback(null, context);
      } catch (error) {
        callback(error);
      }
    };
  }

  getStaticAddress(params: {
    walletName: string;
    storeAs: string;
  }): EngineStep {
    const ee = this.engine?.scenarioEE ?? this.ee;
    return async function (context: SparkContext, callback) {
      try {
        const walletInfo = params.walletName
          ? context.vars?.[params.walletName]
          : context.sparkWallet;
        if (!walletInfo) {
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        if (!walletInfo) {
          throw new Error(`Wallet ${params.walletName || "default"} not found`);
        }

        const { balance } = await walletInfo.wallet.getBalance();

        const wallet: IssuerSparkWallet = walletInfo.wallet;

        const staticAddresses = await wallet.queryStaticDepositAddresses();
        let staticDepostAddress: string;
        if (staticAddresses && staticAddresses.length > 0) {
          staticDepostAddress = staticAddresses[0];
        } else {
          staticDepostAddress = await wallet.getStaticDepositAddress();
        }

        context.vars = context.vars || {};
        context.vars[params.storeAs] = {
          walletName: params.walletName,
          wallet: wallet,
          staticAddress: staticDepostAddress,
          sparkAddress: await wallet.getSparkAddress(),
          balance: balance,
        };

        ee.emit("histogram", "spark.get_static_address_time", Date.now());
        ee.emit("counter", "spark.get_static_address_success", 1);
        callback(null, context);
      } catch (error) {
        ee.emit("counter", "spark.get_static_address_failed", 1);
        callback(error);
      }
    };
  }

  printWalletInfo(params: { storedName: string }): EngineStep {
    const ee = this.engine?.scenarioEE ?? this.ee;
    return async function (context: SparkContext, callback) {
      try {
        const walletInfo: {
          walletName: string;
          wallet: IssuerSparkWallet;
          staticAddress: string;
          sparkAddress: string;
          balance: bigint;
        } = context.vars?.[params.storedName];

        if (!walletInfo) {
          throw new Error(`Wallet ${params.storedName} not found`);
        }

        ee.emit("histogram", "spark.print_wallet_info_time", Date.now());
        ee.emit("counter", "spark.print_wallet_info_success", 1);
        callback(null, context);
      } catch (error) {
        ee.emit("counter", "spark.print_wallet_info_failed", 1);
        callback(error);
      }
    };
  }

  claimStaticDeposit(params: { walletName: string }): EngineStep {
    const ee = this.engine?.scenarioEE ?? this.ee;
    return async function (context: SparkContext, callback) {
      try {
        const walletInfo: {
          wallet: IssuerSparkWallet;
          txId: string;
        } = context.vars?.[params.walletName];

        if (!walletInfo) {
          throw new Error(`Wallet ${params.walletName} not found`);
        }

        const quote = await walletInfo.wallet.getClaimStaticDepositQuote(
          walletInfo.txId,
        );
        if (!quote) {
          throw new Error(
            `No quote found for static deposit claim in wallet ${params.walletName}`,
          );
        }

        await new Promise((resolve) => setTimeout(resolve, 30000));

        const q = await walletInfo.wallet.claimStaticDeposit({
          transactionId: walletInfo.txId,
          creditAmountSats: quote.creditAmountSats,
          sspSignature: quote.signature,
        });

        await new Promise((resolve) => setTimeout(resolve, 60000));

        let pendingTransfer = await (
          walletInfo.wallet as any
        ).transferService.queryTransfer(q.transferId);

        if (!pendingTransfer) {
          throw new Error(`Transfer not found (ID: ${q.transferId})`);
        }

        await (walletInfo.wallet as any).claimTransfer({
          transfer: pendingTransfer,
          optimize: true,
        });

        await new Promise((resolve) => setTimeout(resolve, 1000));
        const { balance: balanceAfterClaim } =
          await walletInfo.wallet.getBalance();

        this.ee.emit(
          "histogram",
          "spark.claim_static_deposit_time",
          Date.now(),
        );
        this.ee.emit("counter", "spark.claim_static_deposit_success", 1);
        callback(null, context);
      } catch (error) {
        this.ee.emit("counter", "spark.claim_static_deposit_failed", 1);
        callback(error);
      }
    };
  }

  withdraw(params?: {
    senderWallet: string;
    receiverWallet: string;
    storeAs: string;
    amount: number;
  }): EngineStep {
    const ee = this.engine?.scenarioEE ?? this.ee;
    return async function (context, callback) {
      try {
        let namedWalletInfo = context.vars?.[params.senderWallet];
        if (!namedWalletInfo) {
          throw new Error(
            `Wallet "${params.senderWallet}" not found. Make sure it's locked first.`,
          );
        }

        const sendWallet: IssuerSparkWallet = namedWalletInfo.wallet;

        const { balance } = await sendWallet.getBalance();
        await new Promise((resolve) => setTimeout(resolve, 2000));

        if (balance.valueOf() <= params.amount) {
          throw new Error(
            `${params.senderWallet} balance is insuficcient: ${balance.toString()} sats, withrawal amount: ${params.amount} sats...`,
          );
        }
        namedWalletInfo = context.vars?.[params.receiverWallet];
        if (!namedWalletInfo) {
          throw new Error(
            `Wallet "${params.receiverWallet}" not found. Make sure it's locked first.`,
          );
        }
        const receiverWallet: IssuerSparkWallet = namedWalletInfo.wallet;
        const staticReceiverAddress = namedWalletInfo.staticAddress;

        const feeQuote = await sendWallet.getWithdrawalFeeQuote({
          amountSats: params.amount,
          withdrawalAddress: staticReceiverAddress,
        });

        const userFee =
          feeQuote.userFeeSlow.originalValue +
          feeQuote.l1BroadcastFeeSlow.originalValue;

        if (userFee >= params.amount) {
          throw new Error("estimated fee exceeds wallet balance");
        }

        const result = await sendWallet.withdraw({
          amountSats: params.amount,
          onchainAddress: staticReceiverAddress,
          feeQuote: feeQuote!,
          exitSpeed: "SLOW" as any,
          deductFeeFromWithdrawalAmount: true,
        });

        await new Promise((resolve) => setTimeout(resolve, 40000));

        context.vars = context.vars || {};
        context.vars[params.storeAs] = {
          wallet: receiverWallet,
          txId: result.coopExitTxid,
        };

        ee.emit("counter", "spark.exit_succesful", 1);
        ee.emit("counter", "spark.amount_withdrawed", Number(params.amount));

        callback(null, context);
      } catch (error) {
        ee.emit("counter", "spark.exit_error", 1);
        callback(error);
      }
    };
  }

  fundWalletPool(params?: {
    pools?: string[];
    minAmount?: number;
  }): EngineStep {
    let ee: ArtilleryEventEmitter;
    const scenarioEE = this.engine?.scenarioEE;
    if (scenarioEE) {
      ee = scenarioEE;
    } else {
      ee = this.engine;
    }
    const cmpBig = (x: bigint, y: bigint) => (x < y ? -1 : x > y ? 1 : 0);
    const minBig = (a: bigint, b: bigint) => (a < b ? a : b);

    const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

    async function runWithConcurrency<T>(
      items: T[],
      limit: number,
      task: (x: T) => Promise<void>,
    ) {
      let i = 0;
      const workers = Array(Math.min(limit, items.length))
        .fill(0)
        .map(async () => {
          while (i < items.length) {
            const idx = i++;
            await task(items[idx]);
          }
        });
      await Promise.all(workers);
    }

    return async (context: SparkContext, callback) => {
      try {
        const minAmount = BigInt(params?.minAmount ?? 4000);
        const poolNames = params?.pools ?? Array.from(walletPools.keys());

        type WalletMeta = {
          wallet: IssuerSparkWallet;
          address: string;
          pool: string;
        };
        const metas: WalletMeta[] = [];

        for (const poolName of poolNames) {
          const pool = walletPools.get(poolName);
          if (!pool) {
            console.warn(`Pool "${poolName}" not found, skip`);
            continue;
          }
          for (const wallet of pool.wallets)
            metas.push({ wallet, address: "", pool: poolName });
        }

        const addrByWallet = new Map<IssuerSparkWallet, string>();
        const poolByWallet = new Map<IssuerSparkWallet, string>();
        const balanceByAddr = new Map<string, bigint>();

        await runWithConcurrency(metas, 12, async (m) => {
          try {
            const address = await m.wallet.getSparkAddress();
            addrByWallet.set(m.wallet, address);
            poolByWallet.set(m.wallet, m.pool);

            const br = await m.wallet.getBalance();
            const bal = BigInt(br.balance);
            balanceByAddr.set(address, bal);
          } catch (e: any) {
            console.error(`Balance load failed: ${e?.message ?? e}`);
          }
        });

        const needsFunding: Array<{
          wallet: IssuerSparkWallet;
          deficit: bigint;
          address: string;
        }> = [];
        const hasSurplus: Array<{
          wallet: IssuerSparkWallet;
          surplus: bigint;
          address: string;
        }> = [];

        let totalDeficit = 0n,
          totalSurplus = 0n;
        for (const { wallet } of metas) {
          const address = addrByWallet.get(wallet);
          if (!address) continue;
          const bal = balanceByAddr.get(address) ?? 0n;

          if (bal < minAmount) {
            const d = minAmount - bal;
            needsFunding.push({ wallet, deficit: d, address });
            totalDeficit += d;
          } else if (bal > minAmount) {
            const s = bal - minAmount;
            hasSurplus.push({ wallet, surplus: s, address });
            totalSurplus += s;
          }
        }

        console.log(
          `Analysis: need=${needsFunding.length}, surplus=${hasSurplus.length}, deficit=${totalDeficit}, surplus=${totalSurplus}`,
        );

        if (needsFunding.length === 0) {
          context.vars ||= {};
          context.vars.rebalanceTransferCount = 0;
          context.vars.rebalanceTotalTransferred = 0;
          return callback(null, context);
        }

        hasSurplus.sort((a, b) => cmpBig(b.surplus, a.surplus));
        type Source = {
          wallet: IssuerSparkWallet;
          available: bigint;
          address: string;
        };
        const sources: Source[] = hasSurplus.map((s) => ({
          wallet: s.wallet,
          available: s.surplus,
          address: s.address,
        }));

        const sumAvailable = sources.reduce((acc, s) => acc + s.available, 0n);

        if (sumAvailable < totalDeficit) {
          const needFromFaucet = totalDeficit - sumAvailable;
          console.log(`Need faucet: ${needFromFaucet}`);

          const allByBalanceAsc = metas
            .map((m) => ({
              wallet: m.wallet,
              address: addrByWallet.get(m.wallet)!,
              bal: balanceByAddr.get(addrByWallet.get(m.wallet)!) ?? 0n,
            }))
            .filter((x) => x.address)
            .sort((a, b) => cmpBig(a.bal, b.bal))
            .slice(0, 10);

          const minPerTx = 1000n;
          const maxPerTx = 50000n;

          const { fundWalletFromGraphQL } = await import(
            "./bitcoin-faucet-wrapper"
          );

          let received = 0n;
          let backoff = 30_000;
          for (
            let idx = 0;
            idx < allByBalanceAsc.length && received < needFromFaucet;
            idx++
          ) {
            const w = allByBalanceAsc[idx].wallet;
            const bal = allByBalanceAsc[idx].bal;
            const address = addrByWallet.get(w)!;
            const remaining = needFromFaucet - received;
            const ask =
              remaining <= maxPerTx
                ? remaining < minPerTx
                  ? minPerTx
                  : remaining
                : maxPerTx;

            try {
              const deposit = await w.getSingleUseDepositAddress();
              const txId = await fundWalletFromGraphQL(deposit, Number(ask));
              ee.emit("counter", "spark.faucet_req", 1);

              await sleep(backoff);

              try {
                const leaves = await w.claimDeposit(txId);
                await sleep(40_000);

                if (leaves?.length) {
                  const newBal = await w.getBalance();
                  const got = newBal.balance - bal;
                  received += got;
                  sources.push({
                    wallet: w,
                    available: got - minAmount,
                    address,
                  });

                  ee.emit("counter", "spark.faucet_ok", 1);
                  console.log(
                    `Faucet: got ${got} to ${address} (total received ${received}/${needFromFaucet})`,
                  );
                  allByBalanceAsc[idx].bal = newBal.balance;
                  backoff = 30_000;
                } else {
                  ee.emit("counter", "spark.faucet_claim_empty", 1);
                }
              } catch (e: any) {
                const msg = String(e?.message ?? e);

                ee.emit("counter", "spark.faucet_claim_failed", 1);
                console.error(`Faucet claim failed: ${msg}`);
              }
            } catch (e: any) {
              const msg = String(e?.message ?? e);
              if (msg.includes("429") || msg.toLowerCase().includes("rate")) {
                ee.emit("counter", "spark.faucet_rate", 1);
                await sleep(backoff);
                backoff = Math.min(backoff * 2, 5 * 60_000);
                idx--;
              } else {
                ee.emit("counter", "spark.faucet_err", 1);
              }
            }
          }

          console.log(`Faucet received: ${received}/${needFromFaucet}`);
        }

        needsFunding.sort((a, b) => cmpBig(b.deficit, a.deficit));
        sources.sort((a, b) => cmpBig(b.available, a.available));

        let i = 0,
          j = 0;
        let transferCount = 0;
        let totalTransferred = 0n;

        while (i < needsFunding.length && j < sources.length) {
          const r = needsFunding[i];
          const s = sources[j];

          const amt = minBig(r.deficit, s.available);
          if (r.address === s.address) {
            i++;
            r.deficit -= amt;
            continue;
          }
          if (r.deficit <= 0n) {
            i++;
            continue;
          }
          if (s.available <= 0n) {
            j++;
            if (j >= sources.length) {
              j = 0;
            }

            continue;
          }

          try {
            console.log(
              `Transfer ${amt} from ${s.address.slice(0, 10)}... (${poolByWallet.get(s.wallet)}) to ${r.address.slice(0, 10)}... (${poolByWallet.get(r.wallet)})`,
            );
            const tr = await s.wallet.transfer({
              receiverSparkAddress: r.address,
              amountSats: Number(amt),
            });

            await sleep(3000);
            const pendingTransfer = await (
              r.wallet as any
            ).transferService.queryTransfer(tr.id);
            if (pendingTransfer) {
              console.log(` Wait for transfer claim...`);
              await (r.wallet as any).claimTransfer({
                transfer: pendingTransfer,
              });
              console.log(
                ` Transfer claimed ${tr.id} from ${s.address} to ${r.address}`,
              );
            }

            r.deficit -= amt;
            s.available -= amt;
            totalTransferred += amt;
            transferCount++;

            ee.emit("counter", "spark.rebalance_transfer_ok", 1);
            ee.emit("counter", "spark.rebalance_amount", Number(amt));
          } catch (error: any) {
            ee.emit("counter", "spark.rebalance_transfer_failed", 1);
            console.error(`Transfer failed: ${error?.message ?? error}`);
            j++;
          }
        }

        for (; i < needsFunding.length; i++) {
          if (needsFunding[i].deficit > 0n) {
            console.warn(
              `Unfunded ${needsFunding[i].address.slice(0, 10)}..., missing ${needsFunding[i].deficit}`,
            );
          }
        }

        console.log(
          `Rebalance done. transfers=${transferCount}, total=${totalTransferred}`,
        );

        context.vars ||= {};
        context.vars.rebalanceTransferCount = transferCount;
        context.vars.rebalanceTotalTransferred = Number(totalTransferred);

        ee.emit("counter", "spark.distribute_and_rebalance_complete", 1);
        ee.emit(
          "histogram",
          "spark.distribute_and_rebalance_total",
          Number(totalTransferred),
        );
        callback(null, context);
      } catch (e) {
        console.error("DistributeAndRebalance failed:", e);
        callback(e as any);
      }
    };
  }

  queryNodes(params: { walletName: string }): EngineStep {
    return async (context: SparkContext, callback): Promise<void> => {
      const ee = this.engine?.scenarioEE ?? this.ee;

      // Initialize event emitter
      try {
        // Get the named wallet from context
        const walletInfo = this.getNamedWalletFromContext(
          { walletName: params.walletName },
          context,
        );

        console.log(`Querying nodes for wallet: ${walletInfo.wallet.address}`);

        // Query nodes for given wallet name
        await (walletInfo as any).wallet.queryNodes(
          { includeParents: false } as any,
          undefined,
          2,
        );

        ee.emit("counter", "spark.successful_query_nodes", 1);
        callback(null, context);
      } catch (error) {
        console.log("Query nodes failed: ", error.message);
        ee.emit("counter", "spark.failed_queries", 1);
        callback(error);
      }
    };
  }

  queryPendingTransfers(params: { walletName: string }): EngineStep {
    return async (context: SparkContext, callback): Promise<void> => {
      const ee = this.engine?.scenarioEE ?? this.ee;
      try {
        const walletInfo = this.getNamedWalletFromContext(
          { walletName: params.walletName },
          context,
        );

        console.log(
          `Querying pending transfers for wallet: ${walletInfo.address}`,
        );
        await (walletInfo as any).wallet.queryPendingTransfers({
          walletName: params.walletName,
        });

        ee.emit("counter", "spark.successful_transfers_queries", 1);
        callback(null, context);
      } catch (error) {
        console.log("Query transfers failed:", error.message);
        ee.emit("counter", "spark.failed_transfers_queries", 1);
        callback(error);
      }
    };
  }

  subscribeToEvents(params: { walletName: string }): EngineStep {
    return async (context: SparkContext, callback): Promise<void> => {
      const ee = this.engine?.scenarioEE ?? this.ee;
      try {
        const walletInfo = this.getNamedWalletFromContext(
          { walletName: params.walletName },
          context,
        );

        console.log(`Subscribing to events for wallet: ${walletInfo.address}`);
        const connection = (walletInfo as any).wallet.connectionManager;
        const coordinatorAddress = (
          walletInfo as any
        ).wallet.config.getCoordinatorAddress();
        const sparkClient =
          await connection.createSparkStreamClient(coordinatorAddress);
        await connection.getStreamChannel(walletInfo.address);

        sparkClient.subscribe_to_events(
          {
            identityPublicKey: await (
              walletInfo as any
            ).wallet.config.signer.getIdentityPublicKey(),
          },
          {
            signal: (walletInfo as any).wallet.streamController?.signal,
          },
        );

        ee.emit("counter", "spark.successful_subscribe_events", 1);
        callback(null, context);
      } catch (error) {
        console.log("Subscription events failed:", error.message);
        ee.emit("counter", "subscription_events_failed", 1);
        callback(error);
      }
    };
  }

  generateDepositAddress(params: { walletName: string }): EngineStep {
    return async (context: SparkContext, callback): Promise<void> => {
      const ee = this.engine?.scenarioEE ?? this.ee;
      try {
        const walletInfo: any = this.getNamedWalletFromContext(
          { walletName: params.walletName },
          context,
        );

        console.log(
          `Generating deposit address for wallet: ${walletInfo.address}`,
        );
        const leafId = randomUUID();
        const pubKey = await (
          walletInfo as any
        ).wallet.config.signer.getPublicKeyFromDerivation({
          type: KeyDerivationType.LEAF,
          path: leafId,
        });
        const depositResp = await (
          walletInfo as any
        ).wallet.depositService.generateDepositAddress({
          signingPubkey: pubKey,
          leafId,
        });

        console.log(`Generated deposit address: ${depositResp}`);

        ee.emit("counter", "spark.successful_deposit_address", 1);
        callback(null, context);
      } catch (error) {
        console.log("Generating deposit address for wallet: ", error.message);
        ee.emit("counter", "spark.failed_queries", 1);
        callback(error);
      }
    };
  }

  queryAllTransfers(params: { walletName: string }): EngineStep {
    return async (context: SparkContext, callback): Promise<void> => {
      const ee = this.engine?.scenarioEE ?? this.ee;
      try {
        const walletInfo: any = this.getNamedWalletFromContext(
          { walletName: params.walletName },
          context,
        );
        console.log(`Querying all transfers for wallet: ${walletInfo.address}`);

        await (walletInfo as any).wallet.transferService.queryAllTransfers();

        ee.emit("counter", "spark.successful_queried_transfers", 1);
        callback(null, context);
      } catch (error) {
        console.log("Querying all transfers failed: ", error.message);
        ee.emit("counter", "spark.failed_query_transfers", 1);
        callback(error);
      }
    };
  }

  public getNamedWalletFromContext(
    params: { walletName: string },
    context: SparkContext,
  ): any {
    const walletInfo = params.walletName
      ? context.vars?.[params.walletName]
      : context.sparkWallet;
    if (!walletInfo) {
      console.error(
        `  ERROR: Wallet "${params.walletName || "default"}" not found in context`,
      );
      console.error(
        `  context.vars keys:`,
        context.vars ? Object.keys(context.vars) : "undefined",
      );
      throw new Error(`Wallet ${params.walletName || "default"} not found`);
    }

    return walletInfo;
  }
}
