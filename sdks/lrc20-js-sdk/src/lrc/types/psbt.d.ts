import { Transaction } from "bitcoinjs-lib";
import { ReceiptProof } from "./receipt-proof";

export interface PSBT {
  tx: Transaction;
  inputReceiptProof?: ReceiptProof;
  outputReceiptProof?: ReceiptProof;
}
