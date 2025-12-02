import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import {
  DefaultSparkSigner,
  SparkValidationError,
  getSparkFrost,
  type SignFrostParams,
  type AggregateFrostParams,
  type IKeyPackage,
} from "@buildonspark/spark-sdk/bare";

/**
 * @deprecated It's no longer necessary to provide BareSparkSigner when
 * initializing a SparkWallet and it will be removed in a future release.
 */
export class BareSparkSigner extends DefaultSparkSigner {
  async signFrost({
    message,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    keyDerivation,
    adaptorPubKey,
  }: SignFrostParams): Promise<Uint8Array> {
    const signingPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);

    if (!signingPrivateKey) {
      throw new SparkValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const commitment = selfCommitment.commitment;
    const nonce = this.commitmentToNonceMap.get(commitment);
    if (!nonce) {
      throw new SparkValidationError("Nonce not found for commitment", {
        field: "nonce",
      });
    }

    const keyPackage: IKeyPackage = {
      secretKey: signingPrivateKey,
      publicKey: publicKey,
      verifyingKey: verifyingKey,
    };

    const sparkFrost = getSparkFrost();
    const result = sparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment: selfCommitment.commitment,
      statechainCommitments,
      adaptorPubKey,
    });

    return result;
  }

  async aggregateFrost({
    message,
    statechainCommitments,
    selfCommitment,
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    publicKey,
    verifyingKey,
    adaptorPubKey,
  }: AggregateFrostParams): Promise<Uint8Array> {
    const sparkFrost = getSparkFrost();
    const result = sparkFrost.aggregateFrost({
      message,
      statechainCommitments,
      selfCommitment: selfCommitment.commitment,
      statechainSignatures,
      selfSignature,
      statechainPublicKeys,
      selfPublicKey: publicKey,
      verifyingKey,
      adaptorPubKey,
    });
    return result;
  }
}
