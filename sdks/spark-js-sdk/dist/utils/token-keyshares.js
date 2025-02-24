import { secp256k1 } from "@noble/curves/secp256k1";
import { bigIntToPrivateKey, recoverSecret, } from "./secret-sharing.js";
export function recoverPrivateKeyFromKeyshares(keyshares, threshold) {
    // Convert keyshares to secret shares format
    const shares = keyshares.map((keyshare) => ({
        fieldModulus: BigInt("0x" + secp256k1.CURVE.n.toString(16)), // secp256k1 curve order
        threshold,
        index: BigInt(keyshare.index),
        share: BigInt("0x" + Buffer.from(keyshare.keyshare).toString("hex")),
        proofs: [],
    }));
    // Recover the secret
    const recoveredKey = recoverSecret(shares);
    // Convert to bytes
    return bigIntToPrivateKey(recoveredKey);
}
//# sourceMappingURL=token-keyshares.js.map