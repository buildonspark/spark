import { SparkWallet } from "@buildonspark/spark-sdk";
import { QueryAllTransfersResponse } from "@buildonspark/spark-sdk/proto/spark";
import { NetworkType } from "@buildonspark/spark-sdk/utils";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "zustand";
import { Currency, CurrencyType } from "../utils/currency";

export const PERMANENT_CURRENCIES: Map<string, Currency> = new Map([
  [
    "BTC",
    {
      name: "Bitcoin",
      code: "BTC",
      decimals: 8,
      type: CurrencyType.BLOCKCHAIN,
      balance: 1231,
      symbol: "₿",
    },
  ],
  [
    "USD",
    {
      name: "US Dollar",
      code: "USD",
      decimals: 2,
      type: CurrencyType.FIAT,
      symbol: "$",
    },
  ],
  // MXPC: {
  //   name: "MXP Coin",
  //   code: "MXPC",
  //   decimals: 2,
  //   type: CurrencyType.TOKEN,
  //   balance: 0,
  //   symbol: "MXN",
  //   logo: <MxnLogo />,
  //   pubkey:
  //     "02773f9ebfaf81126d6dde46227374eac7c2de7f5a99f76a2ef93780e9960e355f",
  //   usdPrice: 20.46,
  // },
  // USDC: {
  //   name: "USD Coin",
  //   code: "USDC",
  //   decimals: 2,
  //   type: CurrencyType.TOKEN,
  //   balance: 0,
  //   symbol: "USDC",
  //   logo: <UsdcLogo />,
  //   pubkey:
  //     "03269c03f240fd2764e048284bceeb09f8b256b60a3bc2737cb119a61127358c1f",
  //   usdPrice: 1.0,
  // },
]);

const getLocalStorageWalletNetwork = (): NetworkType => {
  try {
    const storedNetwork = localStorage.getItem("spark_wallet_network");
    if (storedNetwork !== null) {
      return storedNetwork as NetworkType;
    }
  } catch (e) {
    console.log("Error getting initial wallet network", e);
  }
  return "REGTEST"; // default to mainnet.
};

interface WalletState {
  wallet?: SparkWallet;
  sparkAddress: string;
  initWalletNetwork: NetworkType;
  mnemonic: string | null;
  activeInputCurrency: Currency;
  activeAsset: Currency;
  assets: Map<string, Currency>;
}

interface WalletActions {
  initWallet: (mnemonic: string) => Promise<void>;
  initWalletFromSeed: (seed: string) => Promise<void>;
  setInitWalletNetwork: (network: NetworkType) => void;
  setWallet: (wallet: SparkWallet) => void;
  setSparkAddress: (sparkAddress: string) => void;
  getMasterPublicKey: () => Promise<string>;
  getAllTransfers: (
    limit: number,
    offset: number,
  ) => Promise<QueryAllTransfersResponse>;
  getBitcoinDepositAddress: () => Promise<string>;
  createLightningInvoice: (amount: number, memo: string) => Promise<string>;
  sendTransfer: (amount: number, recipient: string) => Promise<void>;
  payLightningInvoice: (invoice: string) => Promise<void>;
  transferTokens: (
    tokenPublicKey: string,
    tokenAmount: bigint,
    receiverSparkAddress: string,
  ) => Promise<void>;
  setActiveAsset: (asset: Currency) => void;
  updateAssets: (assets: Map<string, Currency>) => void;
  setActiveInputCurrency: (currency: Currency) => void;
  withdrawOnchain: (address: string, amount: number) => Promise<void>;
  loadStoredWallet: () => Promise<boolean>;
}

type WalletStore = WalletState & WalletActions;

const MNEMONIC_STORAGE_KEY = "spark_wallet_mnemonic";
const SEED_STORAGE_KEY = "spark_wallet_seed";

const useWalletStore = create<WalletStore>((set, get) => ({
  initWalletNetwork: getLocalStorageWalletNetwork(),
  setInitWalletNetwork: (network: NetworkType) => {
    set({ initWalletNetwork: network });
  },
  setWallet: (wallet: SparkWallet) => {
    set({ wallet });
  },
  sparkAddress: "",
  setSparkAddress: (sparkAddress: string) => {
    set({ sparkAddress });
  },
  mnemonic: null,
  activeInputCurrency: PERMANENT_CURRENCIES.get("USD")!,
  setActiveInputCurrency: (currency: Currency) => {
    set({ activeInputCurrency: currency });
  },
  assets: PERMANENT_CURRENCIES,
  activeAsset: PERMANENT_CURRENCIES.get("BTC")!,
  setActiveAsset: (asset: Currency) => {
    set({ activeAsset: asset });
  },
  transferTokens: async (
    tokenPublicKey: string,
    tokenAmount: bigint,
    receiverSparkAddress: string,
  ) => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    await wallet.transferTokens({
      tokenPublicKey,
      tokenAmount,
      receiverSparkAddress,
    });
  },
  updateAssets: (newAssets: Map<string, Currency>) => {
    const currentAssets = get().assets;
    newAssets.forEach((value, key) => {
      currentAssets.set(key, value);
    });
    set({ assets: currentAssets });
  },
  btcAddressInfo: {},
  initWallet: async (mnemonic: string) => {
    const { initWalletNetwork, setSparkAddress } = get();

    if (initWalletNetwork === "REGTEST") {
      const { wallet } = await SparkWallet.initialize({
        mnemonicOrSeed: mnemonic,
        options: {
          network: initWalletNetwork,
        },
      });
      set({ wallet });
      setSparkAddress(await wallet.getSparkAddress());
    } else {
      const { wallet } = await SparkWallet.initialize({
        mnemonicOrSeed: mnemonic,
        options: {
          network: initWalletNetwork,
        },
      });
      set({ wallet });
      setSparkAddress(await wallet.getSparkAddress());
    }
    sessionStorage.setItem(MNEMONIC_STORAGE_KEY, mnemonic);
    set({ mnemonic });
  },
  initWalletFromSeed: async (seed: string) => {
    const { initWalletNetwork, setSparkAddress } = get();
    if (initWalletNetwork === "REGTEST") {
      const { wallet } = await SparkWallet.initialize({
        mnemonicOrSeed: seed,
        options: {
          network: initWalletNetwork,
        },
      });
      set({ wallet });
      setSparkAddress(await wallet.getSparkAddress());
    } else {
      const { wallet } = await SparkWallet.initialize({
        mnemonicOrSeed: seed,
        options: {
          network: initWalletNetwork,
        },
      });
      set({ wallet });
      setSparkAddress(await wallet.getSparkAddress());
    }
    sessionStorage.setItem(SEED_STORAGE_KEY, seed);
  },
  getMasterPublicKey: async () => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    return await wallet.getIdentityPublicKey();
  },
  getAllTransfers: async (limit: number, offset: number) => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    return await wallet.getTransfers(limit, offset);
  },
  getBitcoinDepositAddress: async () => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    const btcDepositAddress = await wallet.getDepositAddress();

    if (!btcDepositAddress) {
      throw new Error("Failed to generate deposit address");
    }
    return btcDepositAddress;
  },
  loadStoredWallet: async () => {
    const storedMnemonic = sessionStorage.getItem(MNEMONIC_STORAGE_KEY);
    const storedSeed = sessionStorage.getItem(SEED_STORAGE_KEY);
    if (storedSeed) {
      await get().initWalletFromSeed(storedSeed);
    } else if (storedMnemonic) {
      await get().initWallet(storedMnemonic);
    }
    return true;
  },
  sendTransfer: async (amountSats: number, recipient: string) => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    await wallet.transfer({
      amountSats: amountSats,
      receiverSparkAddress: recipient,
    });
  },
  createLightningInvoice: async (amountSats: number, memo: string) => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    const invoice = await wallet.createLightningInvoice({
      amountSats,
      memo,
    });
    return invoice;
  },
  withdrawOnchain: async (address: string, amount: number) => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    await wallet.withdraw({
      onchainAddress: address,
      targetAmountSats: amount,
    });
  },
  payLightningInvoice: async (invoice: string) => {
    const { wallet } = get();
    if (!wallet) {
      throw new Error("Wallet not initialized");
    }
    await wallet.payLightningInvoice({
      invoice,
    });
  },
}));

export function useWallet() {
  const { wallet } = useWalletStore();
  const queryClient = useQueryClient();

  useQuery({
    queryKey: ["wallet", "init"],
    queryFn: async () => {
      const result = await useWalletStore.getState().loadStoredWallet();
      if (result) {
        queryClient.invalidateQueries({ queryKey: ["wallet", "balance"] });
      }
      return result;
    },
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });

  const balanceQuery = useQuery({
    queryKey: ["wallet", "balance"],
    queryFn: async () => {
      if (!wallet) {
        throw new Error("Wallet not initialized");
      }
      const balance = await wallet.getBalance();
      return balance;
    },
    refetchOnMount: true,
    enabled: !!wallet,
    staleTime: 3000,
    refetchInterval: 3000,
  });

  const satsUsdPriceQuery = useQuery({
    queryKey: ["satsUsdPrice"],
    queryFn: async () => {
      try {
        const response = await fetch(
          "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd",
        );
        if (!response.ok) {
          throw new Error(
            `Failed to fetch BTC price. status: ${response.status}`,
          );
        }
        const data = await response.json();
        if (!data?.bitcoin?.usd) throw new Error("Invalid response format");
        return data.bitcoin.usd / 100_000_000;
      } catch {
        return 0.00091491;
      }
    },
    refetchInterval: 60000,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    staleTime: 60000,
  });

  const state = useWalletStore.getState();

  const initWallet = async (mnemonic: string) => {
    await useWalletStore.getState().initWallet(mnemonic);
    queryClient.invalidateQueries({ queryKey: ["wallet", "balance"] });
  };

  const initWalletFromSeed = async (seed: string) => {
    await useWalletStore.getState().initWalletFromSeed(seed);
    queryClient.invalidateQueries({ queryKey: ["wallet", "balance"] });
  };

  return {
    activeInputCurrency: state.activeInputCurrency,
    setActiveInputCurrency: state.setActiveInputCurrency,
    assets: state.assets,
    activeAsset: state.activeAsset,
    setActiveAsset: state.setActiveAsset,
    btcBalance: {
      value: Number(balanceQuery.data?.balance ?? 0),
      isLoading: balanceQuery.isLoading || !wallet,
      error: balanceQuery.error,
    },
    getAllTransfers: state.getAllTransfers,
    tokenBalances: {
      value: (balanceQuery.data?.tokenBalances ?? new Map()) as Map<
        string,
        { balance: bigint }
      >,
      isLoading: balanceQuery.isLoading || !wallet,
      error: balanceQuery.error,
    },
    satsUsdPrice: {
      value: satsUsdPriceQuery.data ?? 0,
      isLoading: satsUsdPriceQuery.isLoading,
      error: satsUsdPriceQuery.error,
    },
    isInitialized: !!wallet,
    initWallet,
    initWalletFromSeed,
    initWalletNetwork: state.initWalletNetwork,
    sparkAddress: state.sparkAddress,
    setInitWalletNetwork: state.setInitWalletNetwork,
    sendSparkTokenTransfer: state.transferTokens,
    updateAssets: state.updateAssets,
    getMasterPublicKey: state.getMasterPublicKey,
    getBitcoinDepositAddress: state.getBitcoinDepositAddress,
    sendTransfer: state.sendTransfer,
    createLightningInvoice: state.createLightningInvoice,
    payLightningInvoice: state.payLightningInvoice,
    loadStoredWallet: state.loadStoredWallet,
    withdrawOnchain: state.withdrawOnchain,
  };
}

export default useWalletStore;
