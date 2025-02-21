import { createContext, useContext, useState } from "react";
import { SparkWallet } from "spark-sdk";
import { Network } from "spark-sdk/utils";

interface SparkWalletContextType {
  wallet: SparkWallet;
}

export const SparkWalletContext = createContext<SparkWalletContextType | null>(
  null
);

export const useSparkWallet = () => {
  const context = useContext(SparkWalletContext);
  if (!context) {
    throw new Error("useSparkWallet must be used within a SparkWalletProvider");
  }
  return context.wallet;
};

export const SparkWalletProvider = ({
  children,
}: {
  children: React.ReactNode;
}) => {
  const [wallet] = useState<SparkWallet>(
    () => new SparkWallet(Network.REGTEST)
  );
  return (
    <SparkWalletContext.Provider value={{ wallet }}>
      {children}
    </SparkWalletContext.Provider>
  );
};
