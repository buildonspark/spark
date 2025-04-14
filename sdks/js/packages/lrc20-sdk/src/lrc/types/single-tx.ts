import { TxInput } from "bitcoinjs-lib";
import { ReceiptProof } from "./receipt-proof.ts";
import { instanceToPlain } from "class-transformer";

export class SingleInput {
  input: TxInput;
  proof: ReceiptProof;

  constructor(input: TxInput, proof: ReceiptProof) {
    this.input = input;
    this.proof = proof;
  }

  stringify(): string {
    return JSON.stringify(instanceToPlain(this), (_, value) => (typeof value === "bigint" ? Number(value) : value));
  }
}
