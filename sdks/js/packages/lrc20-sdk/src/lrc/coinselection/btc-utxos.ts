import { BitcoinUtxo, Lrc20Utxo } from "../types/index.ts";

export class BtcUtxosCoinSelection {
  private utxos: Array<BitcoinUtxo | Lrc20Utxo>;
  private reservedUtxos: Array<BitcoinUtxo | Lrc20Utxo>;

  constructor(utxos: Array<BitcoinUtxo | Lrc20Utxo>, reservedUtxos: Array<BitcoinUtxo | Lrc20Utxo> = []) {
    this.utxos = utxos;
    this.reservedUtxos = reservedUtxos;
  }

  selectUtxos(
    inputsAmount: number,
    lrc20InputsAmount: number,
    outputsAmount: number,
    feeRateVb: number,
    onlyBtcUtxos = false,
    minimalAmount = 0n,
    customIncrementalValue = 0n,
  ): Array<BitcoinUtxo | Lrc20Utxo> {
    // All inputs + new input
    const numInputs = inputsAmount + lrc20InputsAmount + 1;
    const numOutputs = outputsAmount;
    const transactionSize = BigInt(numInputs) * 68n + BigInt(numOutputs) * 31n + 11n; // TODO: change this formula to the correct one
    const approximateFee = (transactionSize * BigInt(feeRateVb * 10_000)) / 10_000n;
    const defaultIncrementalValue = (68n * BigInt(feeRateVb * 10_000)) / 10_000n;

    return this.coinSelection(
      approximateFee + minimalAmount,
      customIncrementalValue || defaultIncrementalValue,
      onlyBtcUtxos,
    );
  }

  coinSelection(target: bigint, utxoIncrementValue: bigint, onlyBtcUtxos = false): Array<BitcoinUtxo | Lrc20Utxo> {
    // Target would be bigger if we will add the utxoIncrementValue to the target
    const targetOnFirstIteration = target + utxoIncrementValue;
    // Check if total UTXOs value is equal to target
    const utxos = (onlyBtcUtxos ? this.utxos.filter((utxo) => !(utxo instanceof Lrc20Utxo)) : this.utxos).filter(
      (utxo) =>
        this.reservedUtxos.findIndex((reserved) => reserved.txid == utxo.txid && reserved.vout == utxo.vout) == -1,
    );
    const totalValue = utxos.reduce((acc, utxo) => acc + BigInt(utxo.satoshis), 0n);
    if (totalValue === targetOnFirstIteration + BigInt(utxos.length) * utxoIncrementValue) {
      return utxos;
    }

    // If we have less than the target amount of utxos, throw an error
    if (targetOnFirstIteration > totalValue) {
      throw new Error("Not enough UTXOs");
    }

    // Check if any single UTXO has enough value
    const matchedValue = utxos.reverse().find((utxo) => BigInt(utxo.satoshis) >= targetOnFirstIteration);
    if (totalValue > targetOnFirstIteration && matchedValue) {
      return [matchedValue]; // Guaranteed to exist due to check
    }

    return this.greedySelection(utxos, totalValue, targetOnFirstIteration, utxoIncrementValue); // Take the first M UTXOs for initial selection
  }

  greedySelection(
    utxos: Array<BitcoinUtxo | Lrc20Utxo>,
    totalValue: bigint,
    target: bigint,
    utxoIncrementValue: bigint,
  ): Array<BitcoinUtxo | Lrc20Utxo> {
    // Sort UTXOs by descending value
    utxos.sort((a, b) => b.satoshis - a.satoshis);

    const selected: Array<BitcoinUtxo | Lrc20Utxo> = [];
    let currentValue = 0n;

    for (let i = 0; i < utxos.length; i++) {
      if (currentValue <= target) {
        selected.push(utxos[i]);
        currentValue += BigInt(utxos[i].satoshis);
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
