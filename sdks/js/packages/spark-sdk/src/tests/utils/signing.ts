import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import * as btc from "@scure/btc-signer";

import { isEphemeralAnchorOutput } from "../../utils/unilateral-exit.js";

export async function signPsbtWithExternalKey(
  psbtHex: string,
  privateKeyInput: string,
): Promise<string> {
  const tx = btc.Transaction.fromPSBT(hexToBytes(psbtHex), {
    allowUnknown: true,
    allowLegacyWitnessUtxo: true,
    version: 3,
  });
  const privateKey = hexToBytes(privateKeyInput);
  for (let i = 0; i < tx.inputsLength; i++) {
    const input = tx.getInput(i);
    if (
      isEphemeralAnchorOutput(
        input?.witnessUtxo?.script,
        input?.witnessUtxo?.amount,
      )
    ) {
      continue;
    }
    tx.updateInput(i, {
      witnessScript: input?.witnessUtxo?.script,
    });
    tx.signIdx(privateKey, i);
    tx.finalizeIdx(i);
  }
  return bytesToHex(tx.toBytes(true, true));
}
