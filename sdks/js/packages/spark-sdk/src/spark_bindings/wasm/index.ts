import { bytesToHex } from "@noble/curves/utils";

import {
  create_dummy_tx,
  decrypt_ecies,
  DummyTx,
  encrypt_ecies,
  KeyPackage,
  SigningCommitment,
  SigningNonce,
  wasm_aggregate_frost,
  wasm_sign_frost,
} from "../../wasm/spark_bindings.js";
import {
  AggregateFrostParams,
  IKeyPackage,
  ISigningCommitment,
  ISigningNonce,
  SignFrostParams,
} from "../types.js";
import { SparkSdkLogger, LOGGER_NAMES } from "../../utils/logging.js";

function createKeyPackage(params: IKeyPackage): KeyPackage {
  return new KeyPackage(
    params.secretKey,
    params.publicKey,
    params.verifyingKey,
  );
}

function createSigningNonce(params: ISigningNonce): SigningNonce {
  return new SigningNonce(params.hiding, params.binding);
}

function createSigningCommitment(
  params: ISigningCommitment,
): SigningCommitment {
  return new SigningCommitment(params.hiding, params.binding);
}

export function signFrost({
  message,
  keyPackage,
  nonce,
  selfCommitment,
  statechainCommitments,
  adaptorPubKey,
}: SignFrostParams): Uint8Array {
  const logMsg = JSON.stringify({
    message: bytesToHex(message),
    keyPackage: {
      secretKey: bytesToHex(keyPackage.secretKey),
      publicKey: bytesToHex(keyPackage.publicKey),
      verifyingKey: bytesToHex(keyPackage.verifyingKey),
    },
    nonce: {
      hiding: bytesToHex(nonce.hiding),
      binding: bytesToHex(nonce.binding),
    },
    selfCommitment: {
      hiding: bytesToHex(selfCommitment.hiding),
      binding: bytesToHex(selfCommitment.binding),
    },
    statechainCommitments: Object.fromEntries(
      Object.entries(statechainCommitments ?? {}).map(([k, v]) => [
        k,
        {
          hiding: bytesToHex(v.hiding),
          binding: bytesToHex(v.binding),
        },
      ]),
    ),
    adaptorPubKey: adaptorPubKey ? bytesToHex(adaptorPubKey) : undefined,
  });

  SparkSdkLogger.get(LOGGER_NAMES.wasm).trace("signFrost", logMsg);
  const result = wasm_sign_frost(
    message,
    createKeyPackage(keyPackage),
    createSigningNonce(nonce),
    createSigningCommitment(selfCommitment),
    statechainCommitments,
    adaptorPubKey,
  );
  SparkSdkLogger.get(LOGGER_NAMES.wasm).trace(
    "signFrost result",
    bytesToHex(result),
  );

  return result;
}

export function aggregateFrost({
  message,
  statechainCommitments,
  selfCommitment,
  statechainSignatures,
  selfSignature,
  statechainPublicKeys,
  selfPublicKey,
  verifyingKey,
  adaptorPubKey,
}: AggregateFrostParams): Uint8Array {
  const logMsg = JSON.stringify({
    message: bytesToHex(message),
    statechainCommitments: Object.fromEntries(
      Object.entries(statechainCommitments ?? {}).map(([k, v]) => [
        k,
        {
          hiding: bytesToHex(v.hiding),
          binding: bytesToHex(v.binding),
        },
      ]),
    ),
    selfCommitment: {
      hiding: bytesToHex(selfCommitment.hiding),
      binding: bytesToHex(selfCommitment.binding),
    },
    statechainSignatures: Object.fromEntries(
      Object.entries(statechainSignatures ?? {}).map(([k, v]) => [
        k,
        bytesToHex(v),
      ]),
    ),
    selfSignature: bytesToHex(selfSignature),
    statechainPublicKeys: Object.fromEntries(
      Object.entries(statechainPublicKeys ?? {}).map(([k, v]) => [
        k,
        bytesToHex(v),
      ]),
    ),
    selfPublicKey: bytesToHex(selfPublicKey),
    verifyingKey: bytesToHex(verifyingKey),
    adaptorPubKey: adaptorPubKey ? bytesToHex(adaptorPubKey) : undefined,
  });
  SparkSdkLogger.get(LOGGER_NAMES.wasm).trace("aggregateFrost", logMsg);
  const result = wasm_aggregate_frost(
    message,
    statechainCommitments,
    createSigningCommitment(selfCommitment),
    statechainSignatures,
    selfSignature,
    statechainPublicKeys,
    selfPublicKey,
    verifyingKey,
    adaptorPubKey,
  );
  SparkSdkLogger.get(LOGGER_NAMES.wasm).trace(
    "aggregateFrost result",
    bytesToHex(result),
  );
  return result;
}

export function createDummyTx({
  address,
  amountSats,
}: {
  address: string;
  amountSats: bigint;
}): DummyTx {
  return create_dummy_tx(address, amountSats);
}

export function encryptEcies({
  msg,
  publicKey,
}: {
  msg: Uint8Array;
  publicKey: Uint8Array;
}): Uint8Array {
  return encrypt_ecies(msg, publicKey);
}

export function decryptEcies({
  encryptedMsg,
  privateKey,
}: {
  encryptedMsg: Uint8Array;
  privateKey: Uint8Array;
}): Uint8Array {
  return decrypt_ecies(encryptedMsg, privateKey);
}
