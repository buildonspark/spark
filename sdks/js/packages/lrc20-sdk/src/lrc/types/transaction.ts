import { networks } from "bitcoinjs-lib";
import { TxInput } from "./input";
import { TxOutput } from "./output";

export interface TransactionInput {
  privateKeyWIF: string;
  network: networks.Network;
  inputs: TxInput[];
  outputs: TxOutput[];
}

export interface ElectrsTransaction {
  txid: string;
  vin: ElectrsTransactionInput[];
  vout: ElectrsTransactionOutput[];
  status: BitcoinTransactionStatus;
}

export interface ElectrsTransactionInput {
  txid: string;
  vout: number;
}

export interface ElectrsTransactionOutput {
  value: number;
  scriptpubkey_address: string;
}

export interface BitcoinTransactionStatus {
  confirmed: boolean;
}
