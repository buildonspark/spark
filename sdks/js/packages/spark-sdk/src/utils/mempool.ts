import { BitcoinNetwork } from "../types/index.js";
import { getNetworkFromAddress } from "./network.js";

export async function getLatestDepositTxId(
  address: string,
): Promise<string | null> {
  const network = getNetworkFromAddress(address);
  const baseUrl =
    network === BitcoinNetwork.REGTEST
      ? "https://regtest-mempool.dev.dev.sparkinfra.net/api"
      : "https://mempool.space/docs/api";
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
