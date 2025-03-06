import { decode } from "light-bolt11-decoder";
import { Currency } from "./currency";

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

export const decodeLnInvoiceSafely = (invoice: string) => {
  try {
    return decode(invoice);
  } catch (error) {
    return null;
  }
};

/**
 * Formats an asset amount as a fiat currency string
 * @param assetAmount - The amount of the asset
 * @param fiatPerAsset - The fiat value per unit of asset
 * @param fiatAsset - The fiat currency type of type Currency
 * @param includeCode - Whether to include the currency code in the output
 * @returns Formatted fiat amount string
 */
export const formatFiatAmountDisplayString = (
  assetAmount: number,
  fiatPerAsset: number,
  fiatAsset: Currency,
  includeCode: boolean = false,
) => {
  const fiatAmount = new Intl.NumberFormat("en-US", {
    style: "decimal",
    minimumFractionDigits: fiatAsset.decimals,
    maximumFractionDigits: fiatAsset.decimals,
  }).format(assetAmount * fiatPerAsset);
  return `${fiatAmount}${includeCode ? ` ${fiatAsset.code}` : ""}`;
};

/**
 * Formats an asset amount as a fiat currency string
 * @param assetAmount - The amount of the asset
 * @param asset - The asset type of type Currency
 * @param includeCode - Whether to include the currency code in the output
 * @returns Formatted fiat amount string
 */
export const formatAssetAmountDisplayString = (
  assetAmount: number,
  asset: Currency,
  includeCode: boolean = false,
) => {
  let formattedAssetAmount = new Intl.NumberFormat("en-US", {
    style: "decimal",
    minimumFractionDigits: asset.decimals,
    maximumFractionDigits: asset.decimals,
  }).format(
    asset.code === "BTC" && assetAmount >= 100_000
      ? assetAmount / 100_000_000
      : assetAmount,
  );
  // return string with trailing zeros removed
  return `${formattedAssetAmount.replace(/\.?0+$/, "")}${
    includeCode
      ? ` ${asset.code === "BTC" && assetAmount < 100_000 ? "SATs" : asset.code}`
      : ""
  }`;
};
