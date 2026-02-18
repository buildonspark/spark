import { getSparkFrost } from "../spark-bindings/spark-bindings.js";

export function generateSignatureFromExistingAdaptor(
  signature: Uint8Array,
  adaptorPrivateKeyBytes: Uint8Array,
): Uint8Array {
  const sparkFrost = getSparkFrost();
  return sparkFrost.generateSignatureFromExistingAdaptor(
    signature,
    adaptorPrivateKeyBytes,
  );
}

export function generateAdaptorFromSignature(signature: Uint8Array): {
  adaptorSignature: Uint8Array;
  adaptorPrivateKey: Uint8Array;
} {
  const sparkFrost = getSparkFrost();
  return sparkFrost.generateAdaptorFromSignature(signature);
}

export function validateOutboundAdaptorSignature(
  pubkey: Uint8Array,
  hash: Uint8Array,
  signature: Uint8Array,
  adaptorPubkey: Uint8Array,
): boolean {
  const sparkFrost = getSparkFrost();
  return sparkFrost.validateAdaptorSignature(
    pubkey,
    hash,
    signature,
    adaptorPubkey,
  );
}

export function applyAdaptorToSignature(
  pubkey: Uint8Array,
  hash: Uint8Array,
  signature: Uint8Array,
  adaptorPrivateKeyBytes: Uint8Array,
): Uint8Array {
  const sparkFrost = getSparkFrost();
  return sparkFrost.applyAdaptorToSignature(
    pubkey,
    hash,
    signature,
    adaptorPrivateKeyBytes,
  );
}
