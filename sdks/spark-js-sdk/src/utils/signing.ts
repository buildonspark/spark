import { secp256k1 } from "@noble/curves/secp256k1";
import { SigningCommitment, SigningNonce } from "../wasm/spark_bindings";

export function getRandomSigningNonce(): SigningNonce {
  const binding = secp256k1.utils.randomPrivateKey();
  const hiding = secp256k1.utils.randomPrivateKey();
  return createSigningNonce(binding, hiding);
}

export function createSigningNonce(
  binding: Uint8Array,
  hiding: Uint8Array
): SigningNonce {
  if (binding.length !== 32 || hiding.length !== 32) {
    throw new Error("Invalid nonce length");
  }

  return new SigningNonce(hiding, binding);
}

export function getSigningCommitmentFromNonce(
  nonce: SigningNonce
): SigningCommitment {
  const bindingPubKey = secp256k1.getPublicKey(nonce.binding, true);
  const hidingPubKey = secp256k1.getPublicKey(nonce.hiding, true);
  return new SigningCommitment(hidingPubKey, bindingPubKey);
}

export function encodeSigningNonceToBytes(nonce: SigningNonce): Uint8Array {
  return new Uint8Array([...nonce.binding, ...nonce.hiding]);
}

export function decodeBytesToSigningNonce(bytes: Uint8Array): SigningNonce {
  if (bytes.length !== 64) {
    throw new Error("Invalid nonce length");
  }
  return new SigningNonce(bytes.slice(32, 64), bytes.slice(0, 32));
}

export function createSigningCommitment(
  binding: Uint8Array,
  hiding: Uint8Array
): SigningCommitment {
  if (binding.length !== 33 || hiding.length !== 33) {
    throw new Error("Invalid nonce commitment length");
  }
  return new SigningCommitment(hiding, binding);
}

export function encodeSigningCommitmentToBytes(
  commitment: SigningCommitment
): Uint8Array {
  if (commitment.binding.length !== 33 || commitment.hiding.length !== 33) {
    throw new Error("Invalid nonce commitment length");
  }

  return new Uint8Array([...commitment.binding, ...commitment.hiding]);
}

export function decodeBytesToSigningCommitment(
  bytes: Uint8Array
): SigningCommitment {
  if (bytes.length !== 66) {
    throw new Error("Invalid nonce commitment length");
  }
  return new SigningCommitment(bytes.slice(33, 66), bytes.slice(0, 33));
}

export function copySigningNonce(nonce: SigningNonce): SigningNonce {
  return new SigningNonce(nonce.hiding, nonce.binding);
}

export function copySigningCommitment(
  commitment: SigningCommitment
): SigningCommitment {
  return new SigningCommitment(commitment.hiding, commitment.binding);
}
