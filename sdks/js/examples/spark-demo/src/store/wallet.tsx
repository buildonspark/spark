import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "zustand";
import MxnLogo from "../icons/test_logo/MxnLogo";
import UsdcLogo from "../icons/test_logo/UsdcLogo";
import { Currency, CurrencyType } from "../utils/currency";

export const PERMANENT_CURRENCIES: Record<string, Currency> = {
  BTC: {
    name: "Bitcoin",
    code: "BTC",
    decimals: 8,
    type: CurrencyType.BLOCKCHAIN,
    balance: 1231,
    symbol: "₿",
  },
  USD: {
    name: "US Dollar",
    code: "USD",
    decimals: 2,
    type: CurrencyType.FIAT,
    symbol: "$",
  },
  MXPC: {
    name: "MXP Coin",
    code: "MXPC",
    decimals: 2,
    type: CurrencyType.TOKEN,
    balance: 0,
    symbol: "MXN",
    logo: <MxnLogo />,
    pubkey:
      "02773f9ebfaf81126d6dde46227374eac7c2de7f5a99f76a2ef93780e9960e355f",
    usdPrice: 20.46,
  },
  USDC: {
    name: "USD Coin",
    code: "USDC",
    decimals: 2,
    type: CurrencyType.TOKEN,
    balance: 0,
    symbol: "USDC",
    logo: <UsdcLogo />,
    pubkey:
      "03269c03f240fd2764e048284bceeb09f8b256b60a3bc2737cb119a61127358c1f",
    usdPrice: 1.0,
  },
};

interface WalletState {
  wallet: SparkWallet;
  isInitialized: boolean;
  mnemonic: string | null;
  activeInputCurrency: Currency;
  activeAsset: Currency;
  assets: Record<string, Currency>;
}

interface WalletActions {
  initWallet: (mnemonic: string) => Promise<void>;
  initWalletFromSeed: (seed: string) => Promise<void>;
  getMasterPublicKey: () => Promise<string>;
  getBitcoinDepositAddress: () => Promise<string>;
  createLightningInvoice: (amount: number, memo: string) => Promise<string>;
  sendTransfer: (amount: number, recipient: string) => Promise<void>;
  payLightningInvoice: (invoice: string) => Promise<void>;
  getTokenBalance: (tokenPublicKey: string) => Promise<{
    balance: bigint;
    leafCount: number;
  }>;
  transferTokens: (
    tokenPublicKey: string,
    tokenAmount: bigint,
    recipientPublicKey: string,
  ) => Promise<void>;
  setActiveAsset: (asset: Currency) => void;
  setActiveInputCurrency: (currency: Currency) => void;
  withdrawOnchain: (address: string, amount: number) => Promise<void>;
  loadStoredWallet: () => Promise<boolean>;
}

type WalletStore = WalletState & WalletActions;

const MNEMONIC_STORAGE_KEY = "spark_wallet_mnemonic";
const SEED_STORAGE_KEY = "spark_wallet_seed";

const useWalletStore = create<WalletStore>((set, get) => ({
  wallet: new SparkWallet(Network.REGTEST),
  isInitialized: false,
  mnemonic: null,
  activeInputCurrency: PERMANENT_CURRENCIES.USD,
  setActiveInputCurrency: (currency: Currency) => {
    set({ activeInputCurrency: currency });
  },
  assets: PERMANENT_CURRENCIES,
  activeAsset: PERMANENT_CURRENCIES.BTC,
  setActiveAsset: (asset: Currency) => {
    set({ activeAsset: asset });
  },
  getTokenBalance: async (tokenPublicKey: string) => {
    const { wallet } = get();
    const balanceObj = await wallet.getBalance();
    if (!balanceObj.tokenBalances) {
      return {
        balance: BigInt(0),
        leafCount: 0,
      };
    }
    return (
      balanceObj.tokenBalances.get(tokenPublicKey) || {
        balance: BigInt(0),
        leafCount: 0,
      }
    );
  },
  transferTokens: async (
    tokenPublicKey: string,
    tokenAmount: bigint,
    recipientPublicKey: string,
  ) => {
    const { wallet } = get();
    await wallet.transferTokens(
      tokenPublicKey,
      tokenAmount,
      recipientPublicKey,
    );
  },
  btcAddressInfo: {},
  initWallet: async (mnemonic: string) => {
    console.log("initWallet", mnemonic);
    const { wallet } = get();
    await wallet.initWallet(mnemonic);
    sessionStorage.setItem(MNEMONIC_STORAGE_KEY, mnemonic);
    set({ isInitialized: true, mnemonic });
  },
  initWalletFromSeed: async (seed: string) => {
    const { wallet } = get();
    await wallet.initWallet(seed);
    sessionStorage.setItem(SEED_STORAGE_KEY, seed);
    set({ isInitialized: true });
  },
  getMasterPublicKey: async () => {
    const { wallet } = get();
    return await wallet.getIdentityPublicKey();
  },
  getBitcoinDepositAddress: async () => {
    const { wallet } = get();
    const btcDepositAddress = await wallet.getDepositAddress();

    if (!btcDepositAddress) {
      throw new Error("Failed to generate deposit address");
    }
    return btcDepositAddress;
  },
  loadStoredWallet: async () => {
    const storedMnemonic = sessionStorage.getItem(MNEMONIC_STORAGE_KEY);
    const storedSeed = sessionStorage.getItem(SEED_STORAGE_KEY);

    console.log("storedMnemonic", storedMnemonic);
    if (storedSeed) {
      await get().initWalletFromSeed(storedSeed);
    } else if (storedMnemonic) {
      await get().initWallet(storedMnemonic);
    }
    return true;
  },
  sendTransfer: async (amountSats: number, recipient: string) => {
    const { wallet } = get();
    await wallet.sendSparkTransfer({
      amountSats,
      receiverSparkAddress: recipient,
    });
  },
  createLightningInvoice: async (amountSats: number, memo: string) => {
    const { wallet } = get();
    const invoice = await wallet.createLightningInvoice({
      amountSats,
      memo,
    });
    return invoice;
  },
  withdrawOnchain: async (address: string, amount: number) => {
    const { wallet } = get();
    await wallet.withdraw({
      onchainAddress: address,
      targetAmountSats: amount,
    });
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
    queryFn: async () => {
      const result = await useWalletStore.getState().loadStoredWallet();
      if (result) {
        queryClient.invalidateQueries({ queryKey: ["wallet", "balance"] });
      }
    },
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  });

  const balanceQuery = useQuery({
    queryKey: ["wallet", "balance"],
    queryFn: async () => {
      console.log("fetching");
      return await wallet.getBalance();
    },
    refetchOnMount: true,
    enabled: isInitialized,
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
      isLoading: balanceQuery.isLoading || !isInitialized,
      error: balanceQuery.error,
    },
    tokenBalances: {
      value: (balanceQuery.data?.tokenBalances ?? {}) as Record<
        string,
        { balance: bigint; leafCount: number }
      >,
      isLoading: balanceQuery.isLoading || !isInitialized,
      error: balanceQuery.error,
    },
    satsUsdPrice: {
      value: satsUsdPriceQuery.data ?? 0,
      isLoading: satsUsdPriceQuery.isLoading,
      error: satsUsdPriceQuery.error,
    },
    isInitialized,
    getTokenBalance: state.getTokenBalance,
    transferTokens: state.transferTokens,
    initWallet,
    initWalletFromSeed,
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
