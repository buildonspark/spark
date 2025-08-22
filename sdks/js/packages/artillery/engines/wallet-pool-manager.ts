import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { IssuerSparkWalletNoEvents } from "./issuer-wallet-no-events";
import { getLoadtestNetworkConfig } from "./network-config";

type WalletType = IssuerSparkWallet | IssuerSparkWalletNoEvents;

import type { WalletInfo, WalletPool } from "./types/engine";

export class WalletPoolManager {
  private static instance: WalletPoolManager;
  private pools: Map<string, WalletPool> = new Map();
  private namedWallets: Map<string, WalletInfo> = new Map();
  private lockedWallets: Set<any> = new Set();

  private constructor() {}

  static getInstance(): WalletPoolManager {
    if (!WalletPoolManager.instance) {
      WalletPoolManager.instance = new WalletPoolManager();
    }
    return WalletPoolManager.instance;
  }

  async initializePool(name: string, size: number, options?: { mnemonics?: string[] }): Promise<void> {
    if (this.pools.has(name)) {
      console.log(`Pool ${name} already initialized`);
      return;
    }

    const pool: WalletPool = {
      name,
      size,
      wallets: [],
      available: [],
      locked: new Set(),
    };

    console.log(`Initializing pool ${name} with ${size} wallets...`);
    const poolStartTime = Date.now();

    const config = getLoadtestNetworkConfig();

    const walletPromises = Array.from({ length: size }, async (_, i) => {
      const walletStartTime = Date.now();

      console.log(`Starting initialization of wallet ${i + 1}/${size} in pool ${name}`);

      const { wallet } = await IssuerSparkWalletNoEvents.initialize({
        options: config,
        mnemonicOrSeed: options?.mnemonics?.[i],
      });

      const walletEndTime = Date.now();
      console.log(`Wallet ${i + 1}/${size} in pool ${name} initialized in ${walletEndTime - walletStartTime}ms`);

      return wallet;
    });

    const wallets = await Promise.all(walletPromises);

    pool.wallets.push(...wallets);
    pool.available.push(...wallets);

    this.pools.set(name, pool);

    const poolEndTime = Date.now();
    console.log(`Pool ${name} initialized with ${size} wallets in ${poolEndTime - poolStartTime}ms`);
  }

  async initializePoolFromEnv(name: string, amountEnvName: string, options?: { mnemonics?: string[] }): Promise<void> {
    const envValue = process.env[amountEnvName];
    if (!envValue) {
      throw new Error(`Environment variable ${amountEnvName} not found for pool "${name}"`);
    }

    const amount = parseInt(envValue, 10);
    if (isNaN(amount) || amount <= 0) {
      throw new Error(`Invalid pool amount from env var ${amountEnvName}: ${envValue}`);
    }

    console.log(`Using pool amount ${amount} from env var ${amountEnvName}`);
    return this.initializePool(name, amount, options);
  }

  getPool(name: string): WalletPool | undefined {
    return this.pools.get(name);
  }

  getAllPools(): Map<string, WalletPool> {
    return this.pools;
  }

  async lockWallet(poolName: string): Promise<WalletType | null> {
    const pool = this.pools.get(poolName);
    if (!pool || pool.available.length === 0) {
      return null;
    }

    const wallet = pool.available.pop()!;
    pool.locked.add(wallet);
    this.lockedWallets.add(wallet);

    return wallet;
  }

  async unlockWallet(wallet: WalletType): Promise<void> {
    for (const pool of this.pools.values()) {
      if (pool.locked.has(wallet)) {
        pool.locked.delete(wallet);
        pool.available.push(wallet);
        this.lockedWallets.delete(wallet);
        break;
      }
    }
  }

  async unlockAllWallets(): Promise<void> {
    const unlockPromises = [];
    for (const wallet of this.lockedWallets) {
      unlockPromises.push(this.unlockWallet(wallet));
    }
    await Promise.all(unlockPromises);
  }

  async registerNamedWallet(name: string, wallet: WalletType, mnemonic?: string): Promise<void> {
    const address = await wallet.getSparkAddress();
    const publicKey = await wallet.getIdentityPublicKey();
    const { balance } = await wallet.getBalance();

    this.namedWallets.set(name, {
      wallet,
      address,
      publicKey,
      balance,
      mnemonic,
    });
  }

  getNamedWallet(name: string): WalletInfo | undefined {
    return this.namedWallets.get(name);
  }

  getAllNamedWallets(): Map<string, WalletInfo> {
    return this.namedWallets;
  }

  clearAll(): void {
    this.pools.clear();
    this.namedWallets.clear();
    this.lockedWallets.clear();
  }
}
