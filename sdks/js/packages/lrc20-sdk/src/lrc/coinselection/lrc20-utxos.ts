import { Lrc20Utxo, getReceiptDataFromProof } from "../types/index.ts";

export class Lrc20UtxosCoinSelection {
  private utxos: Array<Lrc20Utxo>;

  constructor(utxos: Array<Lrc20Utxo>) {
    this.utxos = utxos;
  }

  selectUtxos(amounts: Array<{ tokenPubkey: string; amount: bigint }>): Array<Lrc20Utxo> {
    const tokensMap = new Map<string, bigint>();
    amounts.forEach((out) => {
      if (tokensMap.has(out.tokenPubkey)) {
        tokensMap.set(out.tokenPubkey, tokensMap.get(out.tokenPubkey)! + out.amount);
        return;
      }
      tokensMap.set(out.tokenPubkey, out.amount);
    });

    let tokens = new Array<{ tokenPubkey: string; amount: bigint }>();
    tokensMap.forEach((value, key) => tokens.push({ tokenPubkey: key, amount: value }));

    return tokens
      .map((token) => {
        const utxosToSpend = [];
        const tokenUtxos = this.utxos.filter(
          (utxo) =>
            getReceiptDataFromProof(utxo.receipt!)!.receipt!.tokenPubkey.inner.toString("hex") === token.tokenPubkey,
        );
        let totalAmount = BigInt(0);
        while (totalAmount < token.amount) {
          if (tokenUtxos.length < 1) {
            throw new Error(`Not enough LRC20 UTXO balance for tokenPubkey ${token.tokenPubkey}`);
          }
          const utxo = tokenUtxos[tokenUtxos.length - 1];
          totalAmount += BigInt(getReceiptDataFromProof(utxo.receipt)!.receipt!.tokenAmount.amount);
          utxosToSpend.push(utxo);
          tokenUtxos.pop();
        }

        return utxosToSpend;
      })
      .flat();
  }

  coinSelection(target: bigint, utxoIncrementValue: bigint): Array<Lrc20Utxo> {
    // Check if total UTXO value is equal to target
    const utxos = this.utxos;
    const totalValue = utxos.reduce((acc, utxo) => acc + utxo.receipt.data.receipt.tokenAmount.amount, 0n);
    if (totalValue === target) {
      return utxos;
    }

    if (target > totalValue) {
      throw new Error("Not enough UTXOs");
    }

    // Check if any single UTXO has enough value
    const matchedValue = utxos.reverse().find((utxo) => utxo.receipt.data.receipt.tokenAmount.amount >= target);
    if (totalValue > target && matchedValue) {
      return [matchedValue]; // Guaranteed to exist due to check
    }

    return this.greedySelection(utxos, totalValue, target, utxoIncrementValue); // Take the first M UTXOs for initial selection
  }

  greedySelection(
    utxos: Array<Lrc20Utxo>,
    totalValue: bigint,
    target: bigint,
    utxoIncrementValue: bigint,
  ): Array<Lrc20Utxo> {
    // Sort UTXOs by descending value
    utxos.sort((a, b) => b.satoshis - a.satoshis);

    const selected: Array<Lrc20Utxo> = [];
    let currentValue = 0n;

    for (let i = 0; i < utxos.length; i++) {
      if (currentValue <= target) {
        selected.push(utxos[i]);
        currentValue += utxos[i].receipt.data.receipt.tokenAmount.amount;
        if (i !== 0) {
          target += utxoIncrementValue;
        }
      }
    }

    if (target > totalValue) {
      throw new Error("Not enough UTXOs");
    }

    return selected;
  }
}
