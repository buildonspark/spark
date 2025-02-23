import { hexToBytes } from "@lightsparkdev/core";
import { bytesToHex } from "@noble/curves/abstract/utils";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { SparkWallet } from "spark-sdk";
import { Network } from "spark-sdk/utils";
import { create } from "zustand";
import { Currency } from "../utils/currency";

interface WalletState {
  wallet: SparkWallet;
  isInitialized: boolean;
  mnemonic: string | null;
  activeCurrency: Currency;
  btcAddressInfo: Record<
    string,
    {
      pubkey: string;
      verifyingKey: string;
    }
  >;
}

interface WalletActions {
  generateMnemonic: () => Promise<string>;
  initWallet: (mnemonic: string) => Promise<void>;
  getMasterPublicKey: () => Promise<string>;
  generateDepositAddress: () => Promise<string>;
  createLightningInvoice: (amount: number, memo: string) => Promise<string>;
  sendTransfer: (amount: number, recipient: string) => Promise<void>;
  payLightningInvoice: (invoice: string) => Promise<void>;
  // fetchOwnedTokens: () => Promise<LeafWithPreviousTransactionData[]>;
  setActiveCurrency: (currency: Currency) => void;
  loadStoredWallet: () => Promise<boolean>;
}

type WalletStore = WalletState & WalletActions;

const MNEMONIC_STORAGE_KEY = "spark_wallet_mnemonic";

const useWalletStore = create<WalletStore>((set, get) => ({
  wallet: new SparkWallet(Network.REGTEST),
  isInitialized: false,
  mnemonic: null,
  activeCurrency: Currency.USD,
  setActiveCurrency: (currency: Currency) => {
    set({ activeCurrency: currency });
  },
  // fetchOwnedTokens: async () => {
  //   const { wallet } = get();
  //   const leaves = await wallet.fetchOwnedTokenLeaves([
  //     await wallet.getMasterPubKey(),
  //   ]);
  //   // TODO: process the leaves into tokens.
  //   return leaves;
  // },
  btcAddressInfo: {},

  generateMnemonic: async () => {
    const { wallet } = get();
    const mnemonic = await wallet.generateMnemonic();
    sessionStorage.setItem(MNEMONIC_STORAGE_KEY, mnemonic);
    set({ mnemonic });
    return mnemonic;
  },
  initWallet: async (mnemonic: string) => {
    const { wallet } = get();
    await wallet.createSparkWallet(mnemonic);
    sessionStorage.setItem(MNEMONIC_STORAGE_KEY, mnemonic);
    set({ isInitialized: true, mnemonic });
  },
  getMasterPublicKey: async () => {
    const { wallet } = get();
    return bytesToHex(await wallet.getMasterPubKey());
  },
  generateDepositAddress: async () => {
    const { wallet } = get();
    const leafPubKey = await wallet.getSigner().generatePublicKey();
    const address = await wallet.generateDepositAddress(leafPubKey);

    if (!address.depositAddress) {
      throw new Error("Failed to generate deposit address");
    }

    set({
      btcAddressInfo: {
        [address.depositAddress.address]: {
          pubkey: bytesToHex(leafPubKey),
          verifyingKey: bytesToHex(address.depositAddress.verifyingKey),
        },
      },
    });

    return address.depositAddress.address;
  },
  loadStoredWallet: async () => {
    const storedMnemonic = sessionStorage.getItem(MNEMONIC_STORAGE_KEY);
    if (storedMnemonic) {
      await get().initWallet(storedMnemonic);
    }
    return true;
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
  const { wallet, isInitialized, btcAddressInfo } = useWalletStore();
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
    queryFn: () => {
      return wallet.getBalance();
    },
    enabled: isInitialized,
  });

  useQuery({
    queryKey: ["wallet", "btcAddressInfo"],
    queryFn: async () => {
      for (const address of Object.keys(btcAddressInfo)) {
        const pendingDepositTx = await wallet.queryPendingDepositTx(address);
        if (pendingDepositTx) {
          try {
            const nodes = await wallet.createTreeRoot(
              hexToBytes(btcAddressInfo[address].pubkey),
              hexToBytes(btcAddressInfo[address].verifyingKey),
              pendingDepositTx.depositTx,
              pendingDepositTx.vout,
            );
            await wallet.transferDepositToSelf(
              nodes.nodes,
              hexToBytes(btcAddressInfo[address].pubkey),
            );

            const updatedAddressInfo = { ...btcAddressInfo };
            delete updatedAddressInfo[address];
            useWalletStore.setState({ btcAddressInfo: updatedAddressInfo });
          } catch (error) {
            console.error("error transferring deposit to self", error);
          }
        }
      }

      return true;
    },
    enabled: isInitialized,
    refetchInterval: 5000,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    staleTime: 5000,
  });

  const satsUsdPriceQuery = useQuery({
    queryKey: ["satsUsdPrice"],
    queryFn: async () => {
      console.log("fetching sats usd price");
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
    },
    refetchInterval: 60000,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
    staleTime: 60000,
  });

  useQuery({
    queryKey: ["wallet", "claimTransfers"],
    queryFn: async () => {
      const claimed = await wallet.claimTransfers();
      if (claimed) {
        queryClient.invalidateQueries({
          queryKey: ["wallet", "balance"],
          exact: true,
        });
      }
      return claimed;
    },
    enabled: isInitialized,
    refetchInterval: 5000,
  });

  const state = useWalletStore.getState();

  return {
    activeCurrency: state.activeCurrency,
    setActiveCurrency: state.setActiveCurrency,
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
    getMasterPublicKey: state.getMasterPublicKey,
    generateDepositAddress: state.generateDepositAddress,
    sendTransfer: state.sendTransfer,
    createLightningInvoice: state.createLightningInvoice,
    payLightningInvoice: state.payLightningInvoice,
    loadStoredWallet: state.loadStoredWallet,
  };
}

export default useWalletStore;
