export interface BasicAuth {
  username: string;
  password: string;
}

function encodeBasicAuth(auth: BasicAuth): string {
  return Buffer.from(`${auth.username}:${auth.password}`).toString("base64");
}

export interface ElectrsTransaction {
  txid: string;
  vin: Array<{ txid: string; vout: number }>;
  vout: Array<{ scriptpubkey_address: string; value: number }>;
  status: { confirmed: boolean };
}

export interface BitcoinUtxo {
  txid: string;
  vout: bigint;
  satoshis: number;
  hex: string;
}

export class ElectrsApi {
  private readonly electrsUrl: string;
  private readonly auth: BasicAuth | null;

  constructor(electrsUrl: string, auth: BasicAuth | null = null) {
    this.electrsUrl = electrsUrl;
    this.auth = auth;
  }

  private getHeaders(): Record<string, string> {
    if (this.auth) {
      return { Authorization: `Basic ${encodeBasicAuth(this.auth)}` };
    }
    return {};
  }

  async sendTransaction(txHex: string): Promise<string> {
    const url = `${this.electrsUrl}/tx`;

    const response = await fetch(url, {
      method: "POST",
      body: txHex,
      headers: this.getHeaders(),
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(
        `Failed to broadcast transaction: ${response.status} - ${errorText}`,
      );
    }

    return response.text();
  }

  async getTransactionHex(txid: string): Promise<string> {
    const url = `${this.electrsUrl}/tx/${txid}/hex`;

    const response = await fetch(url, {
      method: "GET",
      headers: this.getHeaders(),
    });

    if (!response.ok) {
      throw new Error(`Failed to get transaction hex: ${response.status}`);
    }

    return response.text();
  }

  async listTransactions(address: string): Promise<ElectrsTransaction[]> {
    const url = `${this.electrsUrl}/address/${address}/txs`;

    const response = await fetch(url, {
      method: "GET",
      headers: this.getHeaders(),
    });

    if (!response.ok) {
      throw new Error(`Failed to list transactions: ${response.status}`);
    }

    return response.json() as Promise<ElectrsTransaction[]>;
  }

  async fetchUtxos(address: string): Promise<BitcoinUtxo[]> {
    const txs = await this.listTransactions(address);

    const inputs: Array<{ txid: string; vout: number }> = [];
    const outputs = new Map<
      string,
      { txid: string; vout: number; value: number }
    >();

    for (const tx of txs) {
      inputs.push(...tx.vin);

      for (let i = 0; i < tx.vout.length; i++) {
        if (tx.vout[i].scriptpubkey_address === address) {
          const key = `${tx.txid}:${i}`;
          outputs.set(key, { txid: tx.txid, vout: i, value: tx.vout[i].value });
        }
      }
    }

    const utxos: BitcoinUtxo[] = [];
    for (const [key, output] of outputs.entries()) {
      const isSpent = inputs.some(
        (input) => input.txid === output.txid && input.vout === output.vout,
      );
      if (!isSpent) {
        const hex = await this.getTransactionHex(output.txid);
        utxos.push({
          txid: output.txid,
          vout: BigInt(output.vout),
          satoshis: output.value,
          hex,
        });
      }
    }

    return utxos;
  }
}
