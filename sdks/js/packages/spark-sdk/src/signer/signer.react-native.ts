import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { ValidationError } from "../errors/index.js";
import { NativeSparkFrost } from "../spark_bindings/native/index.js";
import { IKeyPackage } from "../spark_bindings/types.js";
import {
  AggregateFrostParams,
  DefaultSparkSigner,
  SignFrostParams,
} from "./signer.js";

export class ReactNativeSparkSigner extends DefaultSparkSigner {
  async signFrost({
    message,
    privateAsPubKey,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Promise<Uint8Array> {
    const privateAsPubKeyHex = bytesToHex(privateAsPubKey);
    const signingPrivateKey =
      this.publicKeyToPrivateKeyMap.get(privateAsPubKeyHex);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const nonce = this.commitmentToNonceMap.get(selfCommitment);
    if (!nonce) {
      throw new ValidationError("Nonce not found for commitment", {
        field: "nonce",
      });
    }

    const keyPackage: IKeyPackage = {
      secretKey: hexToBytes(signingPrivateKey),
      publicKey: publicKey,
      verifyingKey: verifyingKey,
    };

    return NativeSparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment,
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
      selfCommitment,
      selfPublicKey: publicKey,
      selfSignature,
      adaptorPubKey,
    });
  }
}
