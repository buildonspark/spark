import { hexToBytes } from "@lightsparkdev/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { SparkWallet } from "spark-sdk";
import { Network } from "spark-sdk/utils";
import { create } from "zustand";

interface WalletState {
  wallet: SparkWallet;
  isInitialized: boolean;
}

interface WalletActions {
  generateMnemonic: () => Promise<string>;
  initWallet: (mnemonic: string) => Promise<void>;
  createLightningInvoice: (amount: number, memo: string) => Promise<string>;
  sendTransfer: (amount: number, recipient: string) => Promise<void>;
  payLightningInvoice: (invoice: string) => Promise<void>;
}

type WalletStore = WalletState & WalletActions;

const useWalletStore = create<WalletStore>((set, get) => ({
  wallet: new SparkWallet(Network.REGTEST),
  isInitialized: false,

  generateMnemonic: async () => {
    const { wallet } = get();
    const mnemonic = await wallet.generateMnemonic();
    return mnemonic;
  },
  initWallet: async (mnemonic: string) => {
    const { wallet } = get();
    await wallet.createSparkWallet(mnemonic);
    set({ isInitialized: true });
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

  const balanceQuery = useQuery({
    queryKey: ["wallet", "balance"],
    queryFn: () => wallet.getBalance(),
    enabled: isInitialized,
  });

  const btcPriceQuery = useQuery({
    queryKey: ["btcPrice"],
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
      return data.bitcoin.usd;
    },
    refetchInterval: 60000,
  });

  useQuery({
    queryKey: ["wallet", "claimTransfers"],
    queryFn: async () => {
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
      value: balanceQuery.data ?? 0,
      isLoading: balanceQuery.isLoading,
      error: balanceQuery.error,
    },
    btcPrice: {
      value: btcPriceQuery.data ?? 0,
      isLoading: btcPriceQuery.isLoading,
      error: btcPriceQuery.error,
    },
    sendTransfer: state.sendTransfer,
    createLightningInvoice: state.createLightningInvoice,
    payLightningInvoice: state.payLightningInvoice,
  };
}

export default useWalletStore;
