import { sha256 } from "@scure/btc-signer/utils";
export function proofOfPossessionMessageHashForDepositAddress(userPubkey, operatorPubkey, depositAddress) {
    const encoder = new TextEncoder();
    const depositAddressBytes = encoder.encode(depositAddress);
    const proofMsg = new Uint8Array([
        ...userPubkey,
        ...operatorPubkey,
        ...depositAddressBytes,
    ]);
    return sha256(proofMsg);
}
//# sourceMappingURL=proof.js.map