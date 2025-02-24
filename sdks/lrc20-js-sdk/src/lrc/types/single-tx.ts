import { Input } from "bitcoinjs-lib/src/transaction";
import { ReceiptProof } from "./receipt-proof";
import { instanceToPlain } from "class-transformer";

export class SingleInput {
  input: Input;
  proof: ReceiptProof;

  constructor(input: Input, proof: ReceiptProof) {
    this.input = input;
    this.proof = proof;
  }

  stringify(): string {
    return JSON.stringify(instanceToPlain(this), (_, value) => (typeof value === "bigint" ? Number(value) : value));
  }
}
