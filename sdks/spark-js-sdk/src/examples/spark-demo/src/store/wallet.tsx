import { hexToBytes } from "@lightsparkdev/core";
import { bytesToHex } from "@noble/curves/abstract/utils";
import { Transaction } from "@scure/btc-signer";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { SparkWallet } from "spark-sdk";
import { getTxFromRawTxHex, Network } from "spark-sdk/utils";
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
  INOC: {
    name: "MXP Coin",
    code: "MXPC",
    decimals: 2,
    type: CurrencyType.TOKEN,
    balance: 90909,
    symbol: "MXN",
    logo: <MxnLogo />,
    pubkey: "0x1234567890",
  },
  USDC: {
    name: "USD Coin",
    code: "USDC",
    decimals: 2,
    type: CurrencyType.TOKEN,
    balance: 35353,
    symbol: "USDC",
    logo: <UsdcLogo />,
    pubkey: "0x1234567890",
  },
};

interface WalletState {
  wallet: SparkWallet;
  isInitialized: boolean;
  mnemonic: string | null;
  activeInputCurrency: Currency;
  btcAddressInfo: Record<
    string,
    {
      pubkey: string;
      verifyingKey: string;
    }
  >;
  activeAsset: Currency;
  assets: Record<string, Currency>;
}

interface WalletActions {
  initWallet: (mnemonic: string) => Promise<void>;
  initWalletFromSeed: (seed: string) => Promise<void>;
  getMasterPublicKey: () => Promise<string>;
  generateDepositAddress: () => Promise<string>;
  createLightningInvoice: (amount: number, memo: string) => Promise<string>;
  sendTransfer: (amount: number, recipient: string) => Promise<void>;
  payLightningInvoice: (invoice: string) => Promise<void>;
  // fetchOwnedTokens: () => Promise<LeafWithPreviousTransactionData[]>;
  setActiveAsset: (asset: Currency) => void;
  setActiveInputCurrency: (currency: Currency) => void;
  withdrawToBtc: (address: string, amount: number) => Promise<void>;
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
  // fetchOwnedTokens: async () => {
  //   const { wallet } = get();
  //   const leaves = await wallet.fetchOwnedTokenLeaves([
  //     await wallet.getMasterPubKey(),
  //   ]);
  //   // TODO: process the leaves into tokens.
  //   return leaves;
  // },
  btcAddressInfo: {},

  initWallet: async (mnemonic: string) => {
    const { wallet } = get();
    await wallet.initWalletFromMnemonic(mnemonic);
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
  generateDepositAddress: async () => {
    const { wallet } = get();
    const leafPubKey = await wallet.generatePublicKey();
    const address = await wallet.generateDepositAddress(hexToBytes(leafPubKey));

    if (!address.depositAddress) {
      throw new Error("Failed to generate deposit address");
    }

    set({
      btcAddressInfo: {
        [address.depositAddress.address]: {
          pubkey: bytesToHex(hexToBytes(leafPubKey)),
          verifyingKey: bytesToHex(address.depositAddress.verifyingKey),
        },
      },
    });

    return address.depositAddress.address;
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
  withdrawToBtc: async (address: string, amount: number) => {
    console.log("withdrawing to btc", address, amount);
    const { wallet } = get();
    await wallet.coopExit(address, amount);
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
    queryFn: async () => {
      return await wallet.getBalance();
    },
    refetchOnMount: true,
    enabled: isInitialized,
    staleTime: 5000,
  });

  useQuery({
    queryKey: ["wallet", "btcAddressInfo"],
    queryFn: async () => {
      for (const address of Object.keys(btcAddressInfo)) {
        let depositTx: Transaction | null = null;
        let vout = 0;
        try {
          const baseUrl = "https://regtest-mempool.dev.dev.sparkinfra.net/api";
          const auth = btoa("lightspark:TFNR6ZeLdxF9HejW");

          const response = await fetch(`${baseUrl}/address/${address}/txs`, {
            headers: {
              Authorization: `Basic ${auth}`,
              "Content-Type": "application/json",
            },
          });

          const addressTxs = await response.json();

          if (addressTxs && addressTxs.length > 0) {
            const latestTx = addressTxs[0];

            // // Find our output
            const outputIndex = latestTx.vout.findIndex(
              (output: any) => output.scriptpubkey_address === address,
            );

            if (outputIndex === -1) {
              return null;
            }

            const txResponse = await fetch(
              `${baseUrl}/tx/${latestTx.txid}/hex`,
              {
                headers: {
                  Authorization: `Basic ${auth}`,
                  "Content-Type": "application/json",
                },
              },
            );
            const txHex = await txResponse.text();
            depositTx = getTxFromRawTxHex(txHex);
            vout = outputIndex;
            break;
          }
        } catch (error) {
          throw error;
        }

        if (depositTx) {
          try {
            await wallet.finalizeDeposit({
              signingPubKey: hexToBytes(btcAddressInfo[address].pubkey),
              verifyingKey: hexToBytes(btcAddressInfo[address].verifyingKey),
              depositTx,
              vout,
            });

            const updatedAddressInfo = { ...btcAddressInfo };
            delete updatedAddressInfo[address];
            useWalletStore.setState({ btcAddressInfo: updatedAddressInfo });
          } catch (error) {
            console.error("error transferring deposit to self", error);
          }
        }
      }

      queryClient.invalidateQueries({
        queryKey: ["wallet", "balance"],
        exact: true,
      });

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

  const state = useWalletStore.getState();

  return {
    activeInputCurrency: state.activeInputCurrency,
    setActiveInputCurrency: state.setActiveInputCurrency,
    assets: state.assets,
    activeAsset: state.activeAsset,
    setActiveAsset: state.setActiveAsset,
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
    initWallet: state.initWallet,
    initWalletFromSeed: state.initWalletFromSeed,
    getMasterPublicKey: state.getMasterPublicKey,
    generateDepositAddress: state.generateDepositAddress,
    sendTransfer: state.sendTransfer,
    createLightningInvoice: state.createLightningInvoice,
    payLightningInvoice: state.payLightningInvoice,
    loadStoredWallet: state.loadStoredWallet,
    withdrawToBtc: state.withdrawToBtc,
  };
}

export default useWalletStore;
