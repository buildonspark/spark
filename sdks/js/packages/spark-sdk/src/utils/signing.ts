import { secp256k1 } from "@noble/curves/secp256k1";
import { ValidationError } from "../errors/index.js";
import type { SigningCommitment, SigningNonce } from "../signer/signer.js";

export function getRandomSigningNonce(): SigningNonce {
  const binding = secp256k1.utils.randomPrivateKey();
  const hiding = secp256k1.utils.randomPrivateKey();
  return createSigningNonce(binding, hiding);
}

export function createSigningNonce(
  binding: Uint8Array,
  hiding: Uint8Array,
): SigningNonce {
  if (binding.length !== 32 || hiding.length !== 32) {
    throw new ValidationError("Invalid nonce length", {
      field: "nonce",
      value: `binding: ${binding.length}, hiding: ${hiding.length}`,
      expected: "Both binding and hiding should be 32 bytes",
    });
  }

  return {
    binding,
    hiding,
  };
}

export function getSigningCommitmentFromNonce(
  nonce: SigningNonce,
): SigningCommitment {
  const bindingPubKey = secp256k1.getPublicKey(nonce.binding, true);
  const hidingPubKey = secp256k1.getPublicKey(nonce.hiding, true);
  return {
    binding: bindingPubKey,
    hiding: hidingPubKey,
  };
}

export function encodeSigningNonceToBytes(nonce: SigningNonce): Uint8Array {
  return new Uint8Array([...nonce.binding, ...nonce.hiding]);
}

export function decodeBytesToSigningNonce(bytes: Uint8Array): SigningNonce {
  if (bytes.length !== 64) {
    throw new ValidationError("Invalid nonce length", {
      field: "bytes",
      value: bytes.length,
      expected: "64 bytes (32 bytes for binding + 32 bytes for hiding)",
    });
  }
  return {
    binding: bytes.slice(32, 64),
    hiding: bytes.slice(0, 32),
  };
}

export function createSigningCommitment(
  binding: Uint8Array,
  hiding: Uint8Array,
): SigningCommitment {
  if (binding.length !== 33 || hiding.length !== 33) {
    throw new ValidationError("Invalid nonce commitment length", {
      field: "commitment",
      value: `binding: ${binding.length}, hiding: ${hiding.length}`,
      expected:
        "Both binding and hiding should be 33 bytes (compressed public keys)",
    });
  }
  return {
    binding,
    hiding,
  };
}

export function encodeSigningCommitmentToBytes(
  commitment: SigningCommitment,
): Uint8Array {
  if (commitment.binding.length !== 33 || commitment.hiding.length !== 33) {
    throw new ValidationError("Invalid nonce commitment length", {
      field: "commitment",
      value: `binding: ${commitment.binding.length}, hiding: ${commitment.hiding.length}`,
      expected:
        "Both binding and hiding should be 33 bytes (compressed public keys)",
    });
  }

  return new Uint8Array([...commitment.binding, ...commitment.hiding]);
}

export function decodeBytesToSigningCommitment(
  bytes: Uint8Array,
): SigningCommitment {
  if (bytes.length !== 66) {
    throw new ValidationError("Invalid nonce commitment length", {
      field: "bytes",
      value: bytes.length,
      expected: "66 bytes (33 bytes for binding + 33 bytes for hiding)",
    });
  }
  return {
    binding: bytes.slice(33, 66),
    hiding: bytes.slice(0, 33),
  };
}
