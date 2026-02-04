import { newHasher } from "./hashstructure.js";

export function proofOfPossessionMessageHashForDepositAddress(
  userPubkey: Uint8Array,
  operatorPubkey: Uint8Array,
  depositAddress: string,
): Uint8Array {
  const encoder = new TextEncoder();
  const depositAddressBytes = encoder.encode(depositAddress);

  return newHasher(["spark", "deposit", "proof_of_possession"])
    .addBytes(userPubkey)
    .addBytes(operatorPubkey)
    .addBytes(depositAddressBytes)
    .hash();
}
