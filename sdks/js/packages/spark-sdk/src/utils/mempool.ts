import {
  ELECTRS_CREDENTIALS,
  getElectrsUrl,
} from "../services/wallet-config.js";
import { BitcoinFaucet } from "../tests/utils/test-faucet.js";
import { BitcoinNetwork } from "../types/index.js";
import { getFetch } from "./fetch.js";
import { Network, type NetworkType, getNetworkFromAddress } from "./network.js";

type AddressTx = {
  txid: string;
  vout: Array<{ scriptpubkey_address?: string }>;
};

type TxLookupResponse = {
  error?: unknown;
};

/**
 * @deprecated Use `SparkWallet.getUtxosForDepositAddress()` instead.
 * Retrieves the most recent transaction that pays to the given deposit address
 * by querying the electrs HTTP API. Retained only for backwards compatibility
 * and will be removed in a future release.
 */
export async function getLatestDepositTxId(
  address: string,
): Promise<string | null> {
  const { fetch, Headers } = getFetch();
  const network = getNetworkFromAddress(address);
  const baseUrl =
    network === BitcoinNetwork.REGTEST
      ? getElectrsUrl("REGTEST")
      : getElectrsUrl("MAINNET");
  const headers = new Headers();

  if (network === BitcoinNetwork.REGTEST) {
    const auth = btoa(
      `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
    );
    headers.set("Authorization", `Basic ${auth}`);
  }

  const response = await fetch(`${baseUrl}/address/${address}/txs`, {
    headers,
  });

  const addressTxs = await response.json<AddressTx[]>();

  const latestTx = addressTxs[0];
  if (latestTx) {
    const outputIndex: number = latestTx.vout.findIndex(
      (output) => output.scriptpubkey_address === address,
    );

    if (outputIndex === -1) {
      return null;
    }

    return latestTx.txid;
  }
  return null;
}

export async function isTxBroadcast(
  txid: string,
  network: Network,
): Promise<boolean> {
  if (network === Network.LOCAL) {
    try {
      const localFaucet = BitcoinFaucet.getInstance();
      await localFaucet.getRawTransaction(txid);
      return true;
    } catch {
      return false;
    }
  }

  const baseUrl = getElectrsUrl(Network[network] as NetworkType);
  const { fetch, Headers } = getFetch();
  const headers = new Headers();

  if (network === Network.REGTEST) {
    const auth = btoa(
      `${ELECTRS_CREDENTIALS.username}:${ELECTRS_CREDENTIALS.password}`,
    );
    headers.set("Authorization", `Basic ${auth}`);
  }

  const response = await fetch(`${baseUrl}/tx/${txid}`, {
    headers,
  });

  const tx = await response.json<TxLookupResponse>();
  if (tx.error) {
    return false;
  }

  return true;
}
