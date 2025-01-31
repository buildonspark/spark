import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";

import * as btc from "@scure/btc-signer";
import { TransactionOutput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { Network, NetworkConfig } from "./network";

export function getP2TRScriptFromPublicKey(
  pubKey: Uint8Array,
  network: Network
): Uint8Array {
  if (pubKey.length !== 33) {
    throw new Error("Public key must be 33 bytes");
  }

  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const script = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    NetworkConfig[network]
  ).script;
  if (!script) {
    throw new Error("Failed to get P2TR address");
  }
  return script;
}

export function getP2TRAddressFromPublicKey(
  pubKey: Uint8Array,
  network: Network
): string {
  if (pubKey.length !== 33) {
    throw new Error("Public key must be 33 bytes");
  }

  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const address = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    NetworkConfig[network]
  ).address;
  if (!address) {
    throw new Error("Failed to get P2TR address");
  }
  return address;
}

export function getP2TRAddressFromPkScript(
  pkScript: Uint8Array,
  network: Network
): string {
  if (pkScript.length !== 34 || pkScript[0] !== 0x51 || pkScript[1] !== 0x20) {
    throw new Error("Invalid pkscript");
  }

  const parsedScript = btc.OutScript.decode(pkScript);

  return btc.Address(NetworkConfig[network]).encode(parsedScript);
}

export function getTxFromRawTxHex(rawTxHex: string): btc.Transaction {
  const txBytes = hexToBytes(rawTxHex);
  const tx = btc.Transaction.fromRaw(txBytes);

  if (!tx) {
    throw new Error("Failed to parse transaction");
  }
  return tx;
}

export function getTxFromRawTxBytes(rawTxBytes: Uint8Array): btc.Transaction {
  const tx = btc.Transaction.fromRaw(rawTxBytes);
  if (!tx) {
    throw new Error("Failed to parse transaction");
  }
  return tx;
}

export function getSigHashFromTx(
  tx: btc.Transaction,
  inputIndex: number,
  prevOutput: TransactionOutput
): Uint8Array {
  // For Taproot, we use preimageWitnessV1 with SIGHASH_DEFAULT (0x00)
  const prevScript = prevOutput.script;
  if (!prevScript) {
    throw new Error("No script found in prevOutput");
  }

  const amount = prevOutput.amount;
  if (!amount) {
    throw new Error("No amount found in prevOutput");
  }

  return tx.preimageWitnessV1(
    inputIndex,
    new Array(tx.inputsLength).fill(prevScript),
    btc.SigHash.DEFAULT,
    new Array(tx.inputsLength).fill(amount)
  );
}

export function getTxId(tx: btc.Transaction): string {
  return bytesToHex(sha256(sha256(tx.unsignedTx)).reverse());
}

export function getTxIdNoReverse(tx: btc.Transaction): string {
  return bytesToHex(sha256(sha256(tx.unsignedTx)));
}
