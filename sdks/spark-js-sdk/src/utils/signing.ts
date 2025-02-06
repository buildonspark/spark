import { secp256k1 } from "@noble/curves/secp256k1";
import {
  SigningCommitment as WasmSigningCommitment,
  SigningNonce as WasmSigningNonce,
} from "../wasm/spark_bindings";

export function getRandomSigningNonce(): WasmSigningNonce {
  const binding = secp256k1.utils.randomPrivateKey();
  const hiding = secp256k1.utils.randomPrivateKey();
  return createSigningNonce(binding, hiding);
}

export function createSigningNonce(
  binding: Uint8Array,
  hiding: Uint8Array
): WasmSigningNonce {
  if (binding.length !== 32 || hiding.length !== 32) {
    throw new Error("Invalid nonce length");
  }

  return new WasmSigningNonce(hiding, binding);
}

export function getSigningCommitmentFromNonce(
  nonce: WasmSigningNonce
): WasmSigningCommitment {
  const bindingPubKey = secp256k1.getPublicKey(nonce.binding, true);
  const hidingPubKey = secp256k1.getPublicKey(nonce.hiding, true);
  return new WasmSigningCommitment(hidingPubKey, bindingPubKey);
}

export function encodeSigningNonceToBytes(nonce: WasmSigningNonce): Uint8Array {
  return new Uint8Array([...nonce.binding, ...nonce.hiding]);
}

export function decodeBytesToSigningNonce(bytes: Uint8Array): WasmSigningNonce {
  if (bytes.length !== 64) {
    throw new Error("Invalid nonce length");
  }
  return new WasmSigningNonce(bytes.slice(32, 64), bytes.slice(0, 32));
}

export function createSigningCommitment(
  binding: Uint8Array,
  hiding: Uint8Array
): WasmSigningCommitment {
  if (binding.length !== 33 || hiding.length !== 33) {
    throw new Error("Invalid nonce commitment length");
  }
  return new WasmSigningCommitment(hiding, binding);
}

export function encodeSigningCommitmentToBytes(
  commitment: WasmSigningCommitment
): Uint8Array {
  if (commitment.binding.length !== 33 || commitment.hiding.length !== 33) {
    throw new Error("Invalid nonce commitment length");
  }

  return new Uint8Array([...commitment.binding, ...commitment.hiding]);
}

export function decodeBytesToSigningCommitment(
  bytes: Uint8Array
): WasmSigningCommitment {
  if (bytes.length !== 66) {
    throw new Error("Invalid nonce commitment length");
  }
  return new WasmSigningCommitment(bytes.slice(33, 66), bytes.slice(0, 33));
}

export function copySigningNonce(nonce: WasmSigningNonce): WasmSigningNonce {
  return new WasmSigningNonce(nonce.hiding, nonce.binding);
}

export function copySigningCommitment(
  commitment: WasmSigningCommitment
): WasmSigningCommitment {
  return new WasmSigningCommitment(commitment.hiding, commitment.binding);
}
