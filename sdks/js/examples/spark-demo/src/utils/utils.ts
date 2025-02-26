interface BtcPriceUsdResponse {
  bitcoin: {
    usd: number;
  };
}

export const fetchBtcUsdPrice = async (): Promise<number | null> => {
  try {
    const response = await fetch(
      "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd",
    );

    if (!response.ok) {
      console.error(`Failed to fetch BTC price: ${response.statusText}`);
      return null;
    }

    const data: BtcPriceUsdResponse = await response.json();

    if (data.bitcoin && typeof data.bitcoin.usd === "number") {
      return data.bitcoin.usd;
    } else {
      console.error("Unexpected response structure:", data);
      return null;
    }
  } catch (error) {
    console.error("Failed to fetch BTC price:", error);
    return null;
  }
};

export const roundDown = (num: number, decimals: number): number => {
  const factor = 10 ** decimals;
  return Math.floor(num * factor) / factor;
};

export const getFontSizeForCard = (input: string) =>
  Math.max(60 - (input.length - 1) * 5, 30);

export const delay = (ms: number) =>
  new Promise((resolve) => setTimeout(resolve, ms));
