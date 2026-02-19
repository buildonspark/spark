import { getSparkFrost } from "../spark-bindings/spark-bindings.js";
import type { WasmVerifiableSecretShare } from "../spark-bindings/spark-bindings.js";

export type VerifiableSecretShare = WasmVerifiableSecretShare;

export async function splitSecretWithProofs(
  secret: Uint8Array,
  threshold: number,
  numberOfShares: number,
): Promise<VerifiableSecretShare[]> {
  const sparkFrost = getSparkFrost();
  return sparkFrost.splitSecretWithProofs(secret, threshold, numberOfShares);
}

export async function recoverSecret(
  shares: VerifiableSecretShare[],
): Promise<Uint8Array> {
  const sparkFrost = getSparkFrost();
  return sparkFrost.recoverSecret(shares);
}

export async function validateShare(
  share: VerifiableSecretShare,
): Promise<void> {
  const sparkFrost = getSparkFrost();
  return sparkFrost.validateShare(
    share.share,
    share.index,
    share.threshold,
    share.proofs,
  );
}
