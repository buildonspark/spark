import { Transaction } from "bitcoinjs-lib";
import { plainToInstance } from "class-transformer";
import { reverseBuffer } from "../utils/buffer.ts";

export class BitcoinTransactionDto {
  constructor(
    public version: number,
    public lock_time: number,
    public input: Array<TxIn>,
    public output: Array<TxOut>,
  ) {}

  public static fromBitcoinTransaction(tx: Transaction): BitcoinTransactionDto {
    let inputs = new Array<TxIn>();
    let outputs = new Array<TxOut>();

    tx.ins.map((input) => {
      inputs.push({
        previous_output: `${reverseBuffer(input.hash).toString("hex")}:${input.index}`,
        script_sig: input.script.toString("hex"),
        sequence: input.sequence,
        witness: input.witness.map((val) => val.toString("hex")),
      });
    });

    tx.outs.map((out) => {
      outputs.push({
        value: out.value,
        script_pubkey: out.script.toString("hex"),
      });
    });

    return new BitcoinTransactionDto(tx.version, tx.locktime, inputs, outputs);
  }

  public static toTransaction(dto: BitcoinTransactionDto): Transaction {
    const tx = new Transaction();
    tx.version = dto.version;
    tx.locktime = dto.lock_time;

    dto.input.forEach((input) => {
      const [txid, index] = input.previous_output.split(":");
      let inputIndex = tx.addInput(
        reverseBuffer(Buffer.from(txid, "hex")),
        parseInt(index),
        input.sequence,
        Buffer.from(input.script_sig, "hex"),
      );
      tx.ins[inputIndex].witness = input.witness.map((witness) => Buffer.from(witness, "hex"));
    });

    dto.output.forEach((output) => {
      tx.addOutput(Buffer.from(output.script_pubkey, "hex"), output.value);
    });
    return tx;
  }
}

export interface BtcMetadata {
  txid: string;
  feesPaid: number;
  utxosSpent: {
    btc: Array<{ txid: string; vout: number; value: number }>;
    lrc20: Array<{ txid: string; vout: number; value: number }>;
  };
}

interface TxIn {
  previous_output: string;
  script_sig: string;
  sequence: number;
  witness: string[];
}

interface TxOut {
  value: number;
  script_pubkey: string;
}
