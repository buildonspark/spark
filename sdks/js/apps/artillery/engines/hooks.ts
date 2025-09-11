import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { IssuerSparkWalletNoEvents } from "./issuer-wallet-no-events";

type WalletType = IssuerSparkWallet | IssuerSparkWalletNoEvents;

import type { SparkContext, ArtilleryEventEmitter } from "./types";
import { LockManager } from "./lock-manager";

const lockManager = LockManager.getInstance();

export async function createLockFile(
  address: string,
  metadata: any = {},
): Promise<boolean> {
  return lockManager.createLock(address, metadata);
}

export async function removeLockFile(address: string): Promise<boolean> {
  return lockManager.removeLock(address);
}

export async function isLocked(address: string): Promise<boolean> {
  return lockManager.isLocked(address);
}

export async function tryLockOneOf(
  addresses: string[],
  metadata: any = {},
): Promise<string | null> {
  return lockManager.tryLockOneOf(addresses, metadata);
}

export async function getUnlockedAddresses(
  addresses: string[],
): Promise<string[]> {
  return lockManager.getUnlockedAddresses(addresses);
}

async function cleanupStaleLocks(maxAge: number = 3600000): Promise<void> {
  return lockManager.cleanupStaleLocks(maxAge);
}

// Wallet pools organized by pool name
export const walletPools = new Map<
  string,
  {
    wallets: IssuerSparkWallet[];
    available: IssuerSparkWallet[];
  }
>();

// Currently locked wallets with their assigned names
export const lockedWallets = new Map<
  string,
  {
    wallet: IssuerSparkWallet;
    pool: string;
  }
>();

// Global named wallets map
export const globalNamedWallets = new Map<
  string,
  {
    wallet: IssuerSparkWallet;
    address: string;
    publicKey: string;
    balance: bigint;
  }
>();

export const defaultPoolConfigs = [{ name: "default", amount: 50 }];

// Common names for wallet assignments
const WALLET_NAMES = [
  "alice",
  "bob",
  "charlie",
  "diana",
  "eve",
  "frank",
  "grace",
  "henry",
  "iris",
  "jack",
  "kate",
  "leo",
  "mary",
  "noah",
  "olivia",
  "peter",
  "quinn",
  "rose",
  "sam",
  "tina",
  "uma",
  "victor",
  "wendy",
  "xavier",
  "yara",
  "zack",
];

interface PoolConfig {
  name: string;
  amount: number;
  batchSize?: number;
}

interface InitializePoolsAction {
  initializePools: {
    pools?: PoolConfig[];
  };
}

// Types for lockWalletsConfig
type LockWalletConfig = string | { walletName: string; pool?: string };
interface LockWalletsConfig {
  wallets?: LockWalletConfig[];
  interval?: number;
  maxAttempts?: number;
}

function getWalletsToLockConfig(
  lockWalletsConfig: LockWalletsConfig | undefined,
): {
  walletsToLock: LockWalletConfig[];
  pollInterval: number;
  pollMaxAttempts: number;
} {
  let walletsToLock: LockWalletConfig[] = [];
  let pollInterval = 500;
  let pollMaxAttempts = 120;
  if (lockWalletsConfig) {
    walletsToLock = lockWalletsConfig.wallets || [];
    pollInterval = lockWalletsConfig.interval ?? 500;
    pollMaxAttempts = lockWalletsConfig.maxAttempts ?? 120;
  }
  return { walletsToLock, pollInterval, pollMaxAttempts };
}

async function lockWalletByConfig(
  lockConfig: LockWalletConfig,
  walletPoolsParam: Map<
    string,
    { wallets: IssuerSparkWallet[]; available: IssuerSparkWallet[] }
  >,
  lockedWalletsParam: Map<string, { wallet: IssuerSparkWallet; pool: string }>,
  context: SparkContext,
  pollInterval: number,
  pollMaxAttempts: number,
): Promise<{ name: string; success: boolean; error?: string }> {
  let name: string;
  let poolName: string;
  if (typeof lockConfig === "string") {
    name = lockConfig;
    poolName = Array.from(walletPoolsParam.keys())[0] || "default";
  } else {
    name = lockConfig.walletName;
    poolName =
      lockConfig.pool || Array.from(walletPoolsParam.keys())[0] || "default";
  }
  // Get the pool (fall back to default if specific pool not found)
  let pool = walletPoolsParam.get(poolName);
  let actualPoolName = poolName;
  if (!pool) {
    pool = walletPoolsParam.get("default");
    if (!pool) {
      return {
        name,
        success: false,
        error: `No pools available for wallet "${name}"`,
      };
    }
    actualPoolName = "default";
  }
  // Get an available wallet from the pool
  let wallet: any;
  let attempts = 0;

  while (attempts < pollMaxAttempts && !wallet) {
    attempts++;

    // Try to find an unlocked wallet in the preferred pool
    for (let i = pool.wallets.length - 1; i >= 0; i--) {
      const candidateWallet = pool.wallets[i];
      const candidateAddress = await candidateWallet.getSparkAddress();

      // Check if wallet is locked via file system
      if (!(await isLocked(candidateAddress))) {
        // Try to acquire the lock
        const lockMetadata = {
          walletName: name,
          poolName: actualPoolName,
          scenarioId: context._uid || context.vars?.$uuid,
        };

        if (await createLockFile(candidateAddress, lockMetadata)) {
          wallet = candidateWallet;
          // Remove from available array if it's there
          const availableIndex = pool.available.indexOf(candidateWallet);
          if (availableIndex > -1) {
            pool.available.splice(availableIndex, 1);
          }
          break;
        }
      }
    }

    // If no wallet found in preferred pool after 10 attempts, try other pools
    if (!wallet && attempts > 10) {
      for (const [otherPoolName, otherPool] of walletPoolsParam.entries()) {
        if (otherPoolName !== poolName) {
          for (let i = otherPool.wallets.length - 1; i >= 0; i--) {
            const candidateWallet = otherPool.wallets[i];
            const candidateAddress = await candidateWallet.getSparkAddress();

            if (!(await isLocked(candidateAddress))) {
              const lockMetadata = {
                walletName: name,
                poolName: otherPoolName,
                scenarioId: context._uid || context.vars?.$uuid,
              };

              if (await createLockFile(candidateAddress, lockMetadata)) {
                wallet = candidateWallet;
                actualPoolName = otherPoolName;
                // Remove from available array if it's there
                const availableIndex =
                  otherPool.available.indexOf(candidateWallet);
                if (availableIndex > -1) {
                  otherPool.available.splice(availableIndex, 1);
                }
                break;
              }
            }
          }
          if (wallet) break;
        }
      }
    }

    if (!wallet) {
      await new Promise((resolve) => setTimeout(resolve, pollInterval));
    }
  }

  if (!wallet) {
    return {
      name,
      success: false,
      error: `Failed to find available wallet for "${name}" after ${pollMaxAttempts} attempts`,
    };
  }

  const address = await wallet.getSparkAddress();
  const publicKey = await wallet.getIdentityPublicKey();

  lockedWalletsParam.set(name, { wallet, pool: actualPoolName });
  context.scenarioLockedWallets.push(name);
  globalNamedWallets.set(name, {
    wallet,
    address,
    publicKey,
    balance: 0n,
  });

  return { name, success: true };
}

async function lockDefaultWallets(
  context: SparkContext,
  walletPoolsParam: Map<
    string,
    { wallets: IssuerSparkWallet[]; available: IssuerSparkWallet[] }
  >,
  lockedWalletsParam: Map<string, { wallet: IssuerSparkWallet; pool: string }>,
) {
  const namesToLock = WALLET_NAMES.slice(0, Math.floor(Math.random() * 3) + 2);
  for (const name of namesToLock) {
    let wallet: any;
    let usedPoolName: string | undefined;

    // Try to find an unlocked wallet from any pool
    for (const [poolName, pool] of walletPoolsParam.entries()) {
      for (let i = pool.wallets.length - 1; i >= 0; i--) {
        const candidateWallet = pool.wallets[i];
        const candidateAddress = await candidateWallet.getSparkAddress();

        // Check if wallet is locked via file system
        if (!(await isLocked(candidateAddress))) {
          const lockMetadata = {
            walletName: name,
            poolName: poolName,
            scenarioId: context._uid || context.vars?.$uuid,
          };

          if (await createLockFile(candidateAddress, lockMetadata)) {
            wallet = candidateWallet;
            usedPoolName = poolName;
            // Remove from available array if it's there
            const availableIndex = pool.available.indexOf(candidateWallet);
            if (availableIndex > -1) {
              pool.available.splice(availableIndex, 1);
            }
            break;
          }
        }
      }
      if (wallet) break;
    }

    if (wallet && usedPoolName) {
      const address = await wallet.getSparkAddress();
      const publicKey = await wallet.getIdentityPublicKey();

      lockedWalletsParam.set(name, { wallet, pool: usedPoolName });
      context.scenarioLockedWallets.push(name);
      globalNamedWallets.set(name, {
        wallet,
        address,
        publicKey,
        balance: 0n,
      });
    }
  }
}

export async function beforeTest(
  context: SparkContext & { _script?: any; script?: any },
  ee: ArtilleryEventEmitter,
  done: (error?: Error) => void,
) {
  try {
    console.log(`BeforeTest hook called!`);

    if (walletPools.size > 0) {
      done();
      return;
    }

    const network = (process.env.SPARK_NETWORK || "LOCAL") as
      | "MAINNET"
      | "REGTEST"
      | "TESTNET"
      | "SIGNET"
      | "LOCAL";

    // Check if there's an initializePools configuration in the script
    const script = context._script || context.script || context;
    console.log(`BeforeTest: Script keys:`, Object.keys(script || {}));
    const beforeTestActions =
      script?.config?.beforeTest || script?.beforeTest || [];
    console.log(
      `BeforeTest: Found ${beforeTestActions.length} beforeTest actions`,
    );

    const initializePoolsAction = (
      beforeTestActions as InitializePoolsAction[]
    ).find((action) => action.initializePools !== undefined);

    const poolConfigs: PoolConfig[] =
      initializePoolsAction?.initializePools?.pools ?? defaultPoolConfigs;

    // Create each pool
    for (const poolConfig of poolConfigs) {
      const poolName = poolConfig.name;
      const amount = poolConfig.amount;

      const batchSize =
        (poolConfig as PoolConfig).batchSize ||
        parseInt(process.env.WALLET_INIT_BATCH_SIZE || "10");
      console.log(
        `Creating pool "${poolName}" with ${amount} wallets (batch size: ${batchSize})...`,
      );

      const wallets: IssuerSparkWallet[] = [];
      const available: IssuerSparkWallet[] = [];

      // Create wallets in parallel batches for better performance
      for (let i = 0; i < amount; i += batchSize) {
        const currentBatchSize = Math.min(batchSize, amount - i);
        const batchStartTime = Date.now();
        const batchPromises = Array.from(
          { length: currentBatchSize },
          async (_, idx) => {
            const walletStartTime = Date.now();
            console.log(
              `  Starting initialization of wallet ${i + idx + 1}/${amount} in pool "${poolName}"`,
            );
            const { wallet } = await IssuerSparkWalletNoEvents.initialize({
              options: {
                network,
                threshold: 3, // Set threshold to match the number of operators
                tokenSignatures: "SCHNORR" as const,
              },
            });
            const walletEndTime = Date.now();
            console.log(
              `  Wallet ${i + idx + 1}/${amount} in pool "${poolName}" initialized in ${walletEndTime - walletStartTime}ms`,
            );
            return wallet;
          },
        );

        const batchWallets = await Promise.all(batchPromises);
        wallets.push(...batchWallets);
        available.push(...batchWallets);

        const batchEndTime = Date.now();
        console.log(
          `  Generated ${i + currentBatchSize}/${amount} wallets for pool "${poolName}" in ${batchEndTime - batchStartTime}ms`,
        );
      }

      walletPools.set(poolName, { wallets, available });
      console.log(`Pool "${poolName}" created with ${amount} wallets`);
    }

    console.log(
      `BeforeTest: Created ${walletPools.size} pools with total ${Array.from(
        walletPools.values(),
      ).reduce((sum, pool) => sum + pool.wallets.length, 0)} wallets`,
    );
    done();
  } catch (error) {
    console.error("BeforeTest failed:", error);
    done(error);
  }
}

export async function beforeScenario(
  context: SparkContext,
  ee: ArtilleryEventEmitter,
  done: (error?: Error) => void,
) {
  try {
    console.log("BeforeScenario: Locking wallets with names...");
    const scenarioSpec: { lockWallets?: LockWalletsConfig } =
      context?._scenarioSpec || context?.scenario;
    const lockWalletsConfig: LockWalletsConfig | undefined =
      scenarioSpec?.lockWallets;
    context.scenarioLockedWallets = context.scenarioLockedWallets || [];
    const { walletsToLock, pollInterval, pollMaxAttempts } =
      getWalletsToLockConfig(lockWalletsConfig);
    if (walletsToLock.length > 0) {
      const requiredWallets: string[] = [];
      const failedWallets: string[] = [];
      for (const lockConfig of walletsToLock) {
        const result = await lockWalletByConfig(
          lockConfig,
          walletPools,
          lockedWallets,
          context,
          pollInterval,
          pollMaxAttempts,
        );
        requiredWallets.push(result.name);
        if (!result.success) {
          console.error(result.error);
          failedWallets.push(result.name);
        }
      }
      if (failedWallets.length > 0) {
        const error = new Error(
          `Failed to lock required wallets: ${failedWallets.join(", ")}. Scenario requires ${requiredWallets.length} wallets but only ${requiredWallets.length - failedWallets.length} were locked.`,
        );
        console.error(error.message);
        done(error);
        return;
      }
    } else {
      await lockDefaultWallets(context, walletPools, lockedWallets);
    }
    context.vars = context.vars || {};
    context.vars.lockedWalletNames = Array.from(lockedWallets.keys());
    console.log(
      `BeforeScenario: Locked ${lockedWallets.size} wallets with names: ${context.vars.lockedWalletNames.join(", ")}`,
    );
    done();
  } catch (error) {
    console.error("BeforeScenario failed:", error);
    done(error);
  }
}

export async function afterScenario(
  context: SparkContext,
  ee: ArtilleryEventEmitter,
  done: (error?: Error) => void,
) {
  try {
    console.log("AfterScenario: Unlocking wallets...");

    // Only unlock wallets that were locked by this scenario
    const scenarioLockedWallets = context.scenarioLockedWallets || [];

    for (const name of scenarioLockedWallets) {
      const lockedInfo = lockedWallets.get(name);
      if (lockedInfo) {
        // Get wallet address to remove lock file
        const address = await lockedInfo.wallet.getSparkAddress();

        // Remove the lock file
        if (await removeLockFile(address)) {
          console.log(`Removed lock file for wallet "${name}" (${address})`);
        }

        // Close wallet connections to free up resources
        try {
          if (typeof lockedInfo.wallet.cleanupConnections === "function") {
            await lockedInfo.wallet.cleanupConnections();
            console.log(`Closed connections for wallet "${name}"`);
          }
        } catch (error) {
          console.warn(
            `Failed to close connections for wallet "${name}":`,
            error,
          );
        }

        const pool = walletPools.get(lockedInfo.pool);
        if (pool) {
          pool.available.push(lockedInfo.wallet);
          console.log(
            `Unlocked wallet "${name}" back to pool "${lockedInfo.pool}"`,
          );
        }
        lockedWallets.delete(name);
        globalNamedWallets.delete(name);
      }
    }

    context.scenarioLockedWallets = [];

    for (const [poolName, pool] of walletPools.entries()) {
      console.log(
        `Pool "${poolName}": ${pool.available.length}/${pool.wallets.length} available`,
      );
    }

    done();
  } catch (error) {
    console.error("AfterScenario failed:", error);
    done(error);
  }
}

export async function afterTest(
  context: SparkContext,
  ee: ArtilleryEventEmitter,
  done: (error?: Error) => void,
) {
  try {
    console.log("AfterTest: Cleaning up wallet pools...");

    walletPools.clear();
    lockedWallets.clear();
    globalNamedWallets.clear();

    console.log("AfterTest: Cleanup complete");
    done();
  } catch (error) {
    console.error("AfterTest failed:", error);
    done(error);
  }
}
