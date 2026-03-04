import "bare-node-runtime/global";
import Module from "module";
import {
  createDummyTx,
  signFrost,
  aggregateFrost,
  encryptEcies,
  decryptEcies,
  splitSecretWithProofs,
  recoverSecret,
  validateShare,
  constructNodeTxPair,
  constructRefundTxTrio,
  computeMultiInputSighash,
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
      adaptorPubKey || new Uint8Array(0),
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
      adaptorPubKey || new Uint8Array(0),
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

  splitSecretWithProofs(
    secret: Uint8Array,
    threshold: number,
    numShares: number,
  ) {
    const result = splitSecretWithProofs(secret, threshold, numShares);
    return Promise.resolve(
      (
        result as {
          threshold: number;
          index: number;
          share: Uint8Array;
          proofs: Uint8Array[];
        }[]
      ).map((s) => ({
        threshold: s.threshold,
        index: s.index,
        share: new Uint8Array(s.share),
        proofs: s.proofs.map((p: Uint8Array) => new Uint8Array(p)),
      })),
    );
  }

  recoverSecret(
    shares: { threshold: number; index: number; share: Uint8Array }[],
  ) {
    const result = recoverSecret(shares);
    return Promise.resolve(result);
  }

  validateShare(
    share: Uint8Array,
    index: number,
    threshold: number,
    proofs: Uint8Array[],
  ) {
    validateShare(share, index, threshold, proofs);
    return Promise.resolve();
  }

  constructNodeTxPair(
    parentTx: Uint8Array,
    vout: number,
    address: string,
    sequence: number,
    directSequence: number,
    feeSats: bigint,
  ) {
    return constructNodeTxPair(
      new Uint8Array(parentTx),
      vout,
      address,
      sequence,
      directSequence,
      feeSats,
    );
  }

  constructRefundTxTrio(
    cpfpNodeTx: Uint8Array,
    directNodeTx: Uint8Array | null,
    vout: number,
    receivingPubkey: Uint8Array,
    network: string,
    sequence: number,
    directSequence: number,
    feeSats: bigint,
  ) {
    return constructRefundTxTrio(
      new Uint8Array(cpfpNodeTx),
      directNodeTx ? new Uint8Array(directNodeTx) : new Uint8Array(0),
      vout,
      new Uint8Array(receivingPubkey),
      network,
      sequence,
      directSequence,
      feeSats,
    );
  }

  computeMultiInputSighash(
    tx: Uint8Array,
    inputIndex: number,
    prevOutScripts: Uint8Array[],
    prevOutValues: number[],
  ) {
    return computeMultiInputSighash(
      new Uint8Array(tx),
      inputIndex,
      prevOutScripts.map((s) => new Uint8Array(s)),
      prevOutValues,
    );
  }
}

setSparkFrostOnce(new SparkFrostBare());

export * from "@buildonspark/spark-sdk/bare";
export { BareSparkSigner } from "./bare-signer.js";
