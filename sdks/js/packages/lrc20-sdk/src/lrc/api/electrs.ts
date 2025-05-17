import { ElectrsTransaction, BitcoinUtxo, BitcoinUtxoDto, BitcoinUtxoSpentStatus } from "../types/index.ts";
import { basicAuth, BasicAuth } from "./basicAuth.ts";

export class ElectrsApi {
  private readonly electrsUrl: string;
  private readonly auth: BasicAuth | null;

  constructor(electrsUrl: string, auth: BasicAuth | null) {
    this.electrsUrl = electrsUrl;
    this.auth = auth;
  }

  async sendTransaction(txHex: string): Promise<string> {
    const url = `${this.electrsUrl}/tx`;

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
    const url = `${this.electrsUrl}/tx/${txid}/hex`;

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
    const url = `${this.electrsUrl}/address/${address}/utxo`;

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

      const data: Array<BitcoinUtxoDto> = await JSON.parse(await response.text());

      return data.map(BitcoinUtxo.fromBitcoinUtxoDto);
    } catch (error) {
      console.log(error);
    }
  }

  async getSpendingStatus(txid: string, vout: bigint): Promise<BitcoinUtxoSpentStatus> {
    const url = `${this.electrsUrl}/tx/${txid}/outspend/${vout}`;

    const response = await fetch(url, {
      method: "GET",
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }

    const data: BitcoinUtxoSpentStatus = await JSON.parse(await response.text());

    return data;
  }

  async getUtxoValue(txid: string, vout: number): Promise<number> {
    const url = `${this.electrsUrl}/tx/${txid}`;

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

  async listTransactions(address: string): Promise<ElectrsTransaction[]> {
    const url = `${this.electrsUrl}/address/${address}/txs`;
    const response = await fetch(url, {
      method: "GET",
      headers: {
        ...(this.auth ? { Authorization: `Basic ${basicAuth(this.auth)}` } : {}),
      },
    });

    if (!response.ok) {
      throw new Error(`API http call failed: ${response.status}`);
    }
    return await JSON.parse(await response.text());
  }
}
