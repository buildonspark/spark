import { sha256 } from "@scure/btc-signer/utils";

export function proofOfPossessionMessageHashForDepositAddress(
  userPubkey: Uint8Array,
  operatorPubkey: Uint8Array,
  depositAddress: string
): Uint8Array {
  const encoder = new TextEncoder();
  const depositAddressBytes = encoder.encode(depositAddress);

  const proofMsg = new Uint8Array([
    ...userPubkey,
    ...operatorPubkey,
    ...depositAddressBytes,
  ]);
  return sha256(proofMsg);
}
