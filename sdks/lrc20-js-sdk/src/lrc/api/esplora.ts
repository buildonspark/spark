import { basicAuth, BasicAuth } from ".";
import { EsploraTransaction } from "../types";
import { BitcoinUtxo, BitcoinUtxoDto, BitcoinUtxoSpentStatus } from "../types/bitcoin-utxo";

export class EsploraApi {
  private readonly esploraUrl: string;
  private readonly auth: BasicAuth | null;

  constructor(esploraUrl: string, auth: BasicAuth | null) {
    this.esploraUrl = esploraUrl;
    this.auth = auth;
  }

  async sendTransaction(txHex: string): Promise<string> {
    const url = `${this.esploraUrl}/tx`;

    const response = await fetch(url, {
      method: "POST",
      body: txHex,
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }

    const data: string = await response.text();

    return data;
  }

  async getTransactionHex(txid: string): Promise<string> {
    const url = `${this.esploraUrl}/tx/${txid}/hex`;

    const response = await fetch(url, {
      method: "GET",
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }

    const data: string = await response.text();

    return data;
  }

  async listBitcoinUtxo(address: string): Promise<Array<BitcoinUtxo>> {
    const url = `${this.esploraUrl}/address/${address}/utxo`;
    console.log(url);

    try {
      const response = await fetch(url, {
        method: "GET",
        headers: {
          ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
        },
      });

      console.log(response);

      if (!response.ok) {
        throw new Error(`API http call failed: ${response.status}`);
      }

      const data: Array<BitcoinUtxoDto> = await response.json();

      return data.map(BitcoinUtxo.fromBitcoinUtxoDto);
    } catch (error) {
      console.log(error);
    }
  }

  async getSpendingStatus(txid: string, vout: bigint): Promise<BitcoinUtxoSpentStatus> {
    const url = `${this.esploraUrl}/tx/${txid}/outspend/${vout}`;

    const response = await fetch(url, {
      method: "GET",
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }

    const data: BitcoinUtxoSpentStatus = await response.json();

    return data;
  }

  async getUtxoValue(txid: string, vout: number): Promise<number> {
    const url = `${this.esploraUrl}/tx/${txid}`;

    const response = await fetch(url, {
      method: "GET",
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }

    const data: any = await response.json();

    const utxoValue = data.vout[vout].value;

    return utxoValue;
  }

  async listTransactions(address: string): Promise<EsploraTransaction[]> {
    const url = `${this.esploraUrl}/address/${address}/txs`;

    const response = await fetch(url, {
      method: "GET",
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }

    return await response.json();
  }
}
