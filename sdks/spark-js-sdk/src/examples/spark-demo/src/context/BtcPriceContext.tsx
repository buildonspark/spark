import React, { createContext, useState, useEffect, useContext } from "react";
import { fetchBtcUsdPrice } from "../utils/utils";
interface BtcPriceContextType {
  btcUsdPrice: number | null;
  satsUsdPrice: number | null;
  lastSuccessfulFetch: Date | null;
}

const BtcPriceContext = createContext<BtcPriceContextType | null>(null);

export const useBtcPrice = (): BtcPriceContextType => {
  const context = useContext(BtcPriceContext);
  if (!context) {
    throw new Error("useBtcPrice must be used within a BtcPriceProvider");
  }
  return context;
};

export const BtcPriceProvider: React.FC<{
  children: React.ReactNode;
}> = ({ children }) => {
  const [btcUsdPrice, setBtcUsdPrice] = useState<number | null>(null);
  const [lastSuccessfulFetch, setLastSuccessfulFetch] = useState<Date | null>(
    null
  );

  useEffect(() => {
    const fetchPrice = async () => {
      try {
        const price = await fetchBtcUsdPrice();
        setBtcUsdPrice(price);
        setLastSuccessfulFetch(new Date());
      } catch (error) {
        console.error("Failed to fetch BTC price from context:", error);
      }
    };

    fetchPrice();
    const interval = setInterval(fetchPrice, 30_000); // Poll every 60 seconds

    return () => clearInterval(interval); // Cleanup on unmount
  }, []);
  const satsUsdPrice = btcUsdPrice ? btcUsdPrice / 100_000_000 : null;
  return (
    <BtcPriceContext.Provider
      value={{ btcUsdPrice, satsUsdPrice, lastSuccessfulFetch }}
    >
      {children}
    </BtcPriceContext.Provider>
  );
};
