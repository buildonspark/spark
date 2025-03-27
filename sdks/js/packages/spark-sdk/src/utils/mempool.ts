import { BitcoinNetwork } from "../types/index.js";
import { getNetworkFromAddress } from "./network.js";

const DEV_MEMPOOL_URL = "https://regtest-mempool.dev.dev.sparkinfra.net/api";
const PROD_MEMPOOL_URL = "https://regtest-mempool.us-west-2.sparkinfra.net/api";

export const MEMPOOL_URL =
  process.env.NODE_ENV === "development" ? DEV_MEMPOOL_URL : PROD_MEMPOOL_URL;

export async function getLatestDepositTxId(
  address: string,
): Promise<string | null> {
  const network = getNetworkFromAddress(address);
  const baseUrl =
    network === BitcoinNetwork.REGTEST
      ? MEMPOOL_URL
      : "https://mempool.space/api";
  const auth = btoa("spark-sdk:mCMk1JqlBNtetUNy");
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };

  if (network === BitcoinNetwork.REGTEST) {
    headers["Authorization"] = `Basic ${auth}`;
  }
  const response = await fetch(`${baseUrl}/address/${address}/txs`, {
    headers,
  });

  const addressTxs = await response.json();

  if (addressTxs && addressTxs.length > 0) {
    const latestTx = addressTxs[0];

    const outputIndex: number = latestTx.vout.findIndex(
      (output: any) => output.scriptpubkey_address === address,
    );

    if (outputIndex === -1) {
      return null;
    }

    return latestTx.txid;
  }
  return null;
}
