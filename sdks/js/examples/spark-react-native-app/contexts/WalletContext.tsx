import { IssuerSparkWallet } from '@buildonspark/issuer-sdk';
import React, {
  createContext,
  ReactNode,
  useCallback,
  useContext,
  useState,
} from 'react';

// Define the shape of wallet state
interface WalletState {
  wallet: IssuerSparkWallet | null;
  sparkAddress: string | null;
  balance: string | null;
  isConnecting: boolean;
  isLoadingBalance: boolean;
  error: string | null;
}

// Define wallet actions
interface WalletActions {
  connectWallet: (mnemonic?: string) => Promise<void>;
  disconnectWallet: () => void;
  getBalance: () => Promise<void>;
  refreshWallet: () => Promise<void>;
}

// Combined context type
type WalletContextType = WalletState & WalletActions;

// Create the context
const WalletContext = createContext<WalletContextType | undefined>(undefined);

// Provider props
interface WalletProviderProps {
  children: ReactNode;
}

// Provider component
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
          network: 'REGTEST',
        },
        mnemonicOrSeed: mnemonic,
      });

      setWallet(newWallet);

      const addr = await newWallet.getSparkAddress();
      const { balance: bal } = await newWallet.getBalance();

      console.log('Spark address', addr);
      setSparkAddress(addr);
      setBalance(bal.toString());
    } catch (err) {
      console.error('Wallet connection error:', err);
      setError(err instanceof Error ? err.message : 'Failed to connect wallet');
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
      console.warn('No wallet connected');
      return;
    }

    try {
      setIsLoadingBalance(true);
      setError(null);
      const { balance: bal } = await wallet.getBalance();
      setBalance(bal.toString());
    } catch (err) {
      console.error('Get balance error:', err);
      setError(err instanceof Error ? err.message : 'Failed to get balance');
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
    // State
    wallet,
    sparkAddress,
    balance,
    isConnecting,
    isLoadingBalance,
    error,
    // Actions
    connectWallet,
    disconnectWallet,
    getBalance,
    refreshWallet,
  };

  return (
    <WalletContext.Provider value={value}>{children}</WalletContext.Provider>
  );
}

// Custom hook to use the wallet context
export function useWallet() {
  const context = useContext(WalletContext);
  if (context === undefined) {
    throw new Error('useWallet must be used within a WalletProvider');
  }
  return context;
}

// Optional: Export types for use in components
export type { WalletActions, WalletContextType, WalletState };
