import { secp256k1 } from "@noble/curves/secp256k1";
import { numberToBytesBE } from "@noble/curves/abstract/utils";
export function addPublicKeys(a, b) {
    if (a.length !== 33 || b.length !== 33) {
        throw new Error("Public keys must be 33 bytes");
    }
    const pubkeyA = secp256k1.ProjectivePoint.fromHex(a);
    const pubkeyB = secp256k1.ProjectivePoint.fromHex(b);
    return pubkeyA.add(pubkeyB).toRawBytes(true);
}
export function applyAdditiveTweakToPublicKey(pubkey, tweak) {
    if (pubkey.length !== 33) {
        throw new Error("Public key must be 33 bytes");
    }
    if (tweak.length !== 32) {
        throw new Error("Tweak must be 32 bytes");
    }
    const pubkeyPoint = secp256k1.ProjectivePoint.fromHex(pubkey);
    const privTweek = secp256k1.utils.normPrivateKeyToScalar(tweak);
    const pubTweek = secp256k1.getPublicKey(privTweek, true);
    const tweekPoint = secp256k1.ProjectivePoint.fromHex(pubTweek);
    return pubkeyPoint.add(tweekPoint).toRawBytes(true);
}
export function subtractPublicKeys(a, b) {
    if (a.length !== 33 || b.length !== 33) {
        throw new Error("Public keys must be 33 bytes");
    }
    const pubkeyA = secp256k1.ProjectivePoint.fromHex(a);
    const pubkeyB = secp256k1.ProjectivePoint.fromHex(b);
    return pubkeyA.subtract(pubkeyB).toRawBytes(true);
}
export function addPrivateKeys(a, b) {
    if (a.length !== 32 || b.length !== 32) {
        throw new Error("Private keys must be 32 bytes");
    }
    // Convert private keys to scalars (big integers)
    const privA = secp256k1.utils.normPrivateKeyToScalar(a);
    const privB = secp256k1.utils.normPrivateKeyToScalar(b);
    // Add the scalars and reduce modulo the curve order
    const sum = (privA + privB) % secp256k1.CURVE.n;
    // Convert back to bytes
    return numberToBytesBE(sum, 32);
}
export function subtractPrivateKeys(a, b) {
    if (a.length !== 32 || b.length !== 32) {
        throw new Error("Private keys must be 32 bytes");
    }
    const privA = secp256k1.utils.normPrivateKeyToScalar(a);
    const privB = secp256k1.utils.normPrivateKeyToScalar(b);
    const sum = (secp256k1.CURVE.n - privB + privA) % secp256k1.CURVE.n;
    return numberToBytesBE(sum, 32);
}
export function sumOfPrivateKeys(keys) {
    return keys.reduce((sum, key) => {
        if (key.length !== 32) {
            throw new Error("Private keys must be 32 bytes");
        }
        return addPrivateKeys(sum, key);
    });
}
export function lastKeyWithTarget(target, keys) {
    if (target.length !== 32) {
        throw new Error("Target must be 32 bytes");
    }
    const sum = sumOfPrivateKeys(keys);
    return subtractPrivateKeys(target, sum);
}
//# sourceMappingURL=keys.js.map