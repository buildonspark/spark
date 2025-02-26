import { networks } from "bitcoinjs-lib";
import { TxInput } from "./input";
import { TxOutput } from "./output";

export interface TransactionInput {
  privateKeyWIF: string;
  network: networks.Network;
  inputs: TxInput[];
  outputs: TxOutput[];
}

export interface EsploraTransaction {
  txid: string;
  vin: EsploraTransactionInput[];
  vout: EsploraTransactionOutput[];
  status: BitcoinTransactionStatus;
}

export interface EsploraTransactionInput {
  txid: string;
  vout: number;
}

export interface EsploraTransactionOutput {
  value: number;
  scriptpubkey_address: string;
}

export interface BitcoinTransactionStatus {
  confirmed: boolean;
}