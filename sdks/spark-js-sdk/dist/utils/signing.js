import { secp256k1 } from "@noble/curves/secp256k1";
import { SigningCommitment as WasmSigningCommitment, SigningNonce as WasmSigningNonce, } from "../wasm/spark_bindings.js";
export function getRandomSigningNonce() {
    const binding = secp256k1.utils.randomPrivateKey();
    const hiding = secp256k1.utils.randomPrivateKey();
    return createSigningNonce(binding, hiding);
}
export function createSigningNonce(binding, hiding) {
    if (binding.length !== 32 || hiding.length !== 32) {
        throw new Error("Invalid nonce length");
    }
    return {
        binding,
        hiding,
    };
}
export function getSigningCommitmentFromNonce(nonce) {
    const bindingPubKey = secp256k1.getPublicKey(nonce.binding, true);
    const hidingPubKey = secp256k1.getPublicKey(nonce.hiding, true);
    return {
        binding: bindingPubKey,
        hiding: hidingPubKey,
    };
}
export function encodeSigningNonceToBytes(nonce) {
    return new Uint8Array([...nonce.binding, ...nonce.hiding]);
}
export function decodeBytesToSigningNonce(bytes) {
    if (bytes.length !== 64) {
        throw new Error("Invalid nonce length");
    }
    return {
        binding: bytes.slice(32, 64),
        hiding: bytes.slice(0, 32),
    };
}
export function createSigningCommitment(binding, hiding) {
    if (binding.length !== 33 || hiding.length !== 33) {
        throw new Error("Invalid nonce commitment length");
    }
    return {
        binding,
        hiding,
    };
}
export function encodeSigningCommitmentToBytes(commitment) {
    if (commitment.binding.length !== 33 || commitment.hiding.length !== 33) {
        throw new Error("Invalid nonce commitment length");
    }
    return new Uint8Array([...commitment.binding, ...commitment.hiding]);
}
export function decodeBytesToSigningCommitment(bytes) {
    if (bytes.length !== 66) {
        throw new Error("Invalid nonce commitment length");
    }
    return {
        binding: bytes.slice(33, 66),
        hiding: bytes.slice(0, 33),
    };
}
export function createWasmSigningNonce(nonce) {
    return new WasmSigningNonce(nonce.hiding, nonce.binding);
}
export function createWasmSigningCommitment(commitment) {
    return new WasmSigningCommitment(commitment.hiding, commitment.binding);
}
//# sourceMappingURL=signing.js.map