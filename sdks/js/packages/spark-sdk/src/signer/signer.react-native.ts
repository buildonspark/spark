import { ValidationError } from "../errors/index.js";
import { NativeSparkFrost } from "../spark_bindings/native/index.js";
import { IKeyPackage } from "../spark_bindings/types.js";
import { DefaultSparkSigner, TaprootSparkSigner } from "./signer.js";
import type { AggregateFrostParams, SignFrostParams } from "./types.js";

export class ReactNativeSparkSigner extends DefaultSparkSigner {
  async signFrost({
    message,
    keyDerivation,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Promise<Uint8Array> {
    const signingPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const commitment = selfCommitment.commitment;
    const nonce = this.commitmentToNonceMap.get(commitment);
    if (!nonce) {
      throw new ValidationError("Nonce not found for commitment", {
        field: "nonce",
      });
    }

    const keyPackage: IKeyPackage = {
      secretKey: signingPrivateKey,
      publicKey: publicKey,
      verifyingKey: verifyingKey,
    };

    return NativeSparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment: commitment,
      statechainCommitments,
      adaptorPubKey,
    });
  }

  async aggregateFrost({
    message,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
    selfSignature,
    statechainSignatures,
    statechainPublicKeys,
  }: AggregateFrostParams): Promise<Uint8Array> {
    return NativeSparkFrost.aggregateFrost({
      message,
      statechainSignatures,
      statechainPublicKeys,
      verifyingKey,
      statechainCommitments,
      selfCommitment: selfCommitment.commitment,
      selfPublicKey: publicKey,
      selfSignature,
      adaptorPubKey,
    });
  }
}

export class ReactNativeTaprootSparkSigner extends TaprootSparkSigner {
  constructor(useAddressIndex = false) {
    super(useAddressIndex);
  }

  async signFrost({
    message,
    keyDerivation,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Promise<Uint8Array> {
    const signingPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const commitment = selfCommitment.commitment;
    const nonce = this.commitmentToNonceMap.get(commitment);
    if (!nonce) {
      throw new ValidationError("Nonce not found for commitment", {
        field: "nonce",
      });
    }

    const keyPackage: IKeyPackage = {
      secretKey: signingPrivateKey,
      publicKey: publicKey,
      verifyingKey: verifyingKey,
    };

    return NativeSparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment: commitment,
      statechainCommitments,
      adaptorPubKey,
    });
  }

  async aggregateFrost({
    message,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
    selfSignature,
    statechainSignatures,
    statechainPublicKeys,
  }: AggregateFrostParams): Promise<Uint8Array> {
    return NativeSparkFrost.aggregateFrost({
      message,
      statechainSignatures,
      statechainPublicKeys,
      verifyingKey,
      statechainCommitments,
      selfCommitment: selfCommitment.commitment,
      selfPublicKey: publicKey,
      selfSignature,
      adaptorPubKey,
    });
  }
}
