import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import React, {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useState,
} from "react";

interface WalletState {
  wallet: IssuerSparkWallet | null;
  sparkAddress: string | null;
  balance: string | null;
  isConnecting: boolean;
  isLoadingBalance: boolean;
  error: string | null;
}

interface WalletActions {
  connectWallet: (mnemonic?: string) => Promise<void>;
  disconnectWallet: () => void;
  getBalance: () => Promise<void>;
  refreshWallet: () => Promise<void>;
}

type WalletContextType = WalletState & WalletActions;

const WalletContext = createContext<WalletContextType | undefined>(undefined);

interface WalletProviderProps {
  children: ReactNode;
}

export function WalletProvider({ children }: WalletProviderProps) {
  const [wallet, setWallet] = useState<IssuerSparkWallet | null>(null);
  const [sparkAddress, setSparkAddress] = useState<string | null>(null);
  const [balance, setBalance] = useState<string | null>(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [isLoadingBalance, setIsLoadingBalance] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const connectWallet = useCallback(async (mnemonic?: string) => {
    try {
      setIsConnecting(true);
      setIsLoadingBalance(true);
      setError(null);

      const { wallet: newWallet } = await IssuerSparkWallet.initialize({
        options: {
          network: "REGTEST",
        },
        mnemonicOrSeed: mnemonic,
      });

      setWallet(newWallet);

      const addr = await newWallet.getSparkAddress();
      const { balance: bal } = await newWallet.getBalance();

      console.log("Spark address", addr);
      setSparkAddress(addr);
      setBalance(bal.toString());
    } catch (err) {
      console.error("Wallet connection error:", err);
      setError(err instanceof Error ? err.message : "Failed to connect wallet");
    } finally {
      setIsConnecting(false);
      setIsLoadingBalance(false);
    }
  }, []);

  const disconnectWallet = useCallback(() => {
    setWallet(null);
    setSparkAddress(null);
    setBalance(null);
    setError(null);
  }, []);

  const getBalance = useCallback(async () => {
    if (!wallet) {
      console.warn("No wallet connected");
      return;
    }

    try {
      setIsLoadingBalance(true);
      setError(null);
      const { balance: bal } = await wallet.getBalance();
      setBalance(bal.toString());
    } catch (err) {
      console.error("Get balance error:", err);
      setError(err instanceof Error ? err.message : "Failed to get balance");
    } finally {
      setIsLoadingBalance(false);
    }
  }, [wallet]);

  const refreshWallet = useCallback(async () => {
    if (wallet) {
      await getBalance();
    }
  }, [wallet, getBalance]);

  const value: WalletContextType = {
    wallet,
    sparkAddress,
    balance,
    isConnecting,
    isLoadingBalance,
    error,
    connectWallet,
    disconnectWallet,
    getBalance,
    refreshWallet,
  };

  return (
    <WalletContext.Provider value={value}>{children}</WalletContext.Provider>
  );
}

export function useWallet() {
  const context = useContext(WalletContext);
  if (context === undefined) {
    throw new Error("useWallet must be used within a WalletProvider");
  }
  return context;
}

export type { WalletActions, WalletContextType, WalletState };
