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
 * @returns {amount: number, code: string, displayString: string}
 */
export const formatFiatAmount = (
  assetAmount: number,
  fiatPerAsset: number,
  fiatAsset: Currency,
  includeCode: boolean = false,
) => {
  const amount = assetAmount * fiatPerAsset;
  const fiatAmount = new Intl.NumberFormat("en-US", {
    style: "decimal",
    minimumFractionDigits: fiatAsset.decimals,
    maximumFractionDigits: fiatAsset.decimals,
  }).format(amount);
  const code = includeCode ? ` ${fiatAsset.code}` : "";
  const displayString = `${fiatAmount}${code}`;

  return {
    amount: roundDown(amount, fiatAsset.decimals ?? 2),
    code,
    displayString,
  };
};

/**
 * Formats an asset amount as a fiat currency string
 * @param assetAmount - The amount of the asset
 * @param asset - The asset type of type Currency
 * @param includeCode - Whether to include the currency code in the output
 * @returns {amount: number, code: string, displayString: string}
 */
export const formatAssetAmount = (
  assetAmount: number,
  asset: Currency,
  includeCode: boolean = false,
) => {
  const amount =
    asset.code === "BTC" && assetAmount >= 100_000
      ? assetAmount / 100_000_000
      : assetAmount;
  let formattedAssetAmount = new Intl.NumberFormat("en-US", {
    style: "decimal",
    minimumFractionDigits: asset.decimals,
    maximumFractionDigits: asset.decimals,
  }).format(amount);
  // return string with trailing zeros removed
  const amountString = formattedAssetAmount.replace(/\.?0+$/, "");
  const code = includeCode
    ? ` ${asset.code === "BTC" && assetAmount < 100_000 ? "SATs" : asset.code}`
    : "";
  const displayString = `${amountString}${code}`;
  return {
    amount: roundDown(amount, asset.decimals ?? 8),
    code,
    displayString,
  };
};
