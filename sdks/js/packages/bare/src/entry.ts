import "bare-node-runtime/global";
import Module from "module";
import {
  createDummyTx,
  signFrost,
  aggregateFrost,
  encryptEcies,
  decryptEcies,
} from "@buildonspark/spark-frost-bare-addon";
import {
  SparkFrostBase,
  setSparkFrostOnce,
  type SignFrostBindingParams,
  type AggregateFrostBindingParams,
} from "@buildonspark/spark-sdk/bare";

/* Avoid a console.error that comes from an import of Node.js require-in-the-middle module, see LIG-8098 */
Object.defineProperty(Module, "_resolveFilename", {
  value: () => {
    throw new Error(
      "@buildonspark/bare: This method is not supported in bare.",
    );
  },
  writable: false,
  enumerable: false,
  configurable: false,
});

class SparkFrostBare extends SparkFrostBase {
  signFrost({
    message,
    keyPackage,
    nonce,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostBindingParams) {
    const statechainCommitmentsArr = statechainCommitments
      ? Object.entries(statechainCommitments)
      : [];
    const result = signFrost(
      message,
      keyPackage,
      nonce,
      selfCommitment,
      statechainCommitmentsArr,
      adaptorPubKey || null,
    );
    return result;
  }

  aggregateFrost({
    message,
    statechainCommitments,
    selfCommitment,
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    selfPublicKey,
    verifyingKey,
    adaptorPubKey,
  }: AggregateFrostBindingParams) {
    const statechainCommitmentsArr = statechainCommitments
      ? Object.entries(statechainCommitments)
      : [];
    const statechainSignaturesArr = statechainSignatures
      ? Object.entries(statechainSignatures)
      : [];
    const statechainPublicKeysArr = statechainPublicKeys
      ? Object.entries(statechainPublicKeys)
      : [];
    const result = aggregateFrost(
      message,
      statechainCommitmentsArr,
      selfCommitment,
      statechainSignaturesArr,
      selfSignature,
      statechainPublicKeysArr,
      selfPublicKey,
      verifyingKey,
      adaptorPubKey || null,
    );
    return result;
  }

  createDummyTx(address, amountSats) {
    return createDummyTx(address, amountSats);
  }
  encryptEcies(msg, publicKey) {
    return encryptEcies(msg, publicKey);
  }
  decryptEcies(encryptedMsg, privateKey) {
    return decryptEcies(encryptedMsg, privateKey);
  }
}

setSparkFrostOnce(new SparkFrostBare());

export * from "@buildonspark/spark-sdk/bare";
export { BareSparkSigner } from "./bare-signer.js";
