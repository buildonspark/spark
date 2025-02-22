import { hexToBytes } from "@lightsparkdev/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { SparkWallet } from "spark-sdk";
import { Network } from "spark-sdk/utils";
import { create } from "zustand";

interface WalletState {
  wallet: SparkWallet;
  isInitialized: boolean;
  mnemonic: string | null;
}

interface WalletActions {
  generateMnemonic: () => Promise<string>;
  initWallet: (mnemonic: string) => Promise<void>;
  createLightningInvoice: (amount: number, memo: string) => Promise<string>;
  sendTransfer: (amount: number, recipient: string) => Promise<void>;
  payLightningInvoice: (invoice: string) => Promise<void>;
  loadStoredWallet: () => Promise<void>;
}

type WalletStore = WalletState & WalletActions;

const MNEMONIC_STORAGE_KEY = "spark_wallet_mnemonic";

const useWalletStore = create<WalletStore>((set, get) => ({
  wallet: new SparkWallet(Network.REGTEST),
  isInitialized: false,
  mnemonic: null,

  generateMnemonic: async () => {
    const { wallet } = get();
    const mnemonic = await wallet.generateMnemonic();
    localStorage.setItem(MNEMONIC_STORAGE_KEY, mnemonic);
    set({ mnemonic });
    return mnemonic;
  },
  initWallet: async (mnemonic: string) => {
    const { wallet } = get();
    await wallet.createSparkWallet(mnemonic);
    localStorage.setItem(MNEMONIC_STORAGE_KEY, mnemonic);
    set({ isInitialized: true, mnemonic });
  },
  loadStoredWallet: async () => {
    const storedMnemonic = localStorage.getItem(MNEMONIC_STORAGE_KEY);
    if (storedMnemonic) {
      await get().initWallet(storedMnemonic);
    }
  },
  sendTransfer: async (amount: number, recipient: string) => {
    const { wallet } = get();
    await wallet.sendTransfer({
      amount,
      receiverPubKey: hexToBytes(recipient),
    });
  },
  createLightningInvoice: async (amountSats: number, memo: string) => {
    const { wallet } = get();
    const invoice = await wallet.createLightningInvoice({
      amountSats,
      memo,
      expirySeconds: 60 * 60 * 24,
    });
    return invoice;
  },
  payLightningInvoice: async (invoice: string) => {
    const { wallet } = get();
    await wallet.payLightningInvoice({
      invoice,
    });
  },
}));

export function useWallet() {
  const { wallet, isInitialized } = useWalletStore();
  const queryClient = useQueryClient();

  useQuery({
    queryKey: ["wallet", "init"],
    queryFn: () => useWalletStore.getState().loadStoredWallet(),
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });

  const balanceQuery = useQuery({
    queryKey: ["wallet", "balance"],
    queryFn: () => wallet.getBalance(),
    enabled: isInitialized,
  });

  const satsUsdPriceQuery = useQuery({
    queryKey: ["satsUsdPrice"],
    queryFn: async () => {
      const response = await fetch(
        "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd"
      );
      if (!response.ok) {
        throw new Error(
          `Failed to fetch BTC price. status: ${response.status}`
        );
      }
      const data = await response.json();
      if (!data?.bitcoin?.usd) throw new Error("Invalid response format");
      return data.bitcoin.usd / 100_000_000;
    },
    refetchInterval: 60000,
  });

  useQuery({
    queryKey: ["wallet", "claimTransfers"],
    queryFn: async () => {
      console.log("testing");
      const claimed = await wallet.claimTransfers();
      if (claimed) {
        queryClient.invalidateQueries({ queryKey: ["wallet", "balance"] });
      }
      return claimed;
    },
    enabled: isInitialized,
    refetchInterval: 5000,
  });

  const state = useWalletStore.getState();

  return {
    balance: {
      value: Number(balanceQuery.data ?? 0),
      isLoading: balanceQuery.isLoading,
      error: balanceQuery.error,
    },
    satsUsdPrice: {
      value: satsUsdPriceQuery.data ?? 0,
      isLoading: satsUsdPriceQuery.isLoading,
      error: satsUsdPriceQuery.error,
    },
    generatorMnemonic: state.generateMnemonic,
    initWallet: async (mnemonic: string) => {
      await state.initWallet(mnemonic);
    },
    sendTransfer: state.sendTransfer,
    createLightningInvoice: state.createLightningInvoice,
    payLightningInvoice: state.payLightningInvoice,
    loadStoredWallet: state.loadStoredWallet,
  };
}

export default useWalletStore;
