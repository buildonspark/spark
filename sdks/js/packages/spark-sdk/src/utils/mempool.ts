import {
  ELECTRS_CREDENTIALS,
  getElectrsUrl,
} from "../services/wallet-config.js";
import { BitcoinNetwork } from "../types/index.js";
import { getNetworkFromAddress } from "./network.js";

export async function getLatestDepositTxId(
  address: string,
): Promise<string | null> {
  const network = getNetworkFromAddress(address);
  const baseUrl =
    network === BitcoinNetwork.REGTEST
      ? getElectrsUrl("REGTEST")
      : getElectrsUrl("MAINNET");
  const headers: Record<string, string> = {};

  if (network === BitcoinNetwork.REGTEST) {
    const auth = btoa(
      `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
    );
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
