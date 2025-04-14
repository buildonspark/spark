import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";

import * as btc from "@scure/btc-signer";
import { TransactionOutput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { getNetwork, Network } from "./network.js";

// const t = tapTweak(pubKey, h); // t = int_from_bytes(tagged_hash("TapTweak", pubkey + h)
// const P = u.lift_x(u.bytesToNumberBE(pubKey)); // P = lift_x(int_from_bytes(pubkey))
// const Q = P.add(Point.fromPrivateKey(t)); // Q = point_add(P, point_mul(G, t))
export function computeTaprootKeyNoScript(pubkey: Uint8Array): Uint8Array {
  if (pubkey.length !== 32) {
    throw new Error("Public key must be 32 bytes");
  }

  const taggedHash = schnorr.utils.taggedHash("TapTweak", pubkey);
  const tweak = bytesToNumberBE(taggedHash);

  // Get the original point
  const P = schnorr.utils.lift_x(schnorr.utils.bytesToNumberBE(pubkey));

  // Add the tweak times the generator point
  const Q = P.add(secp256k1.ProjectivePoint.fromPrivateKey(tweak));

  return Q.toRawBytes();
}

export function getP2TRScriptFromPublicKey(
  pubKey: Uint8Array,
  network: Network,
): Uint8Array {
  if (pubKey.length !== 33) {
    throw new Error("Public key must be 33 bytes");
  }

  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const script = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    getNetwork(network),
  ).script;
  if (!script) {
    throw new Error("Failed to get P2TR address");
  }
  return script;
}

export function getP2TRAddressFromPublicKey(
  pubKey: Uint8Array,
  network: Network,
): string {
  if (pubKey.length !== 33) {
    throw new Error("Public key must be 33 bytes");
  }

  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const address = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    getNetwork(network),
  ).address;
  if (!address) {
    throw new Error("Failed to get P2TR address");
  }
  return address;
}

export function getP2TRAddressFromPkScript(
  pkScript: Uint8Array,
  network: Network,
): string {
  if (pkScript.length !== 34 || pkScript[0] !== 0x51 || pkScript[1] !== 0x20) {
    throw new Error("Invalid pkscript");
  }

  const parsedScript = btc.OutScript.decode(pkScript);

  return btc.Address(getNetwork(network)).encode(parsedScript);
}

export function getP2WPKHAddressFromPublicKey(
  pubKey: Uint8Array,
  network: Network,
): string {
  if (pubKey.length !== 33) {
    throw new Error("Public key must be 33 bytes");
  }

  const address = btc.p2wpkh(pubKey, getNetwork(network)).address;
  if (!address) {
    throw new Error("Failed to get P2WPKH address");
  }
  return address;
}

export function getTxFromRawTxHex(rawTxHex: string): btc.Transaction {
  const txBytes = hexToBytes(rawTxHex);
  const tx = btc.Transaction.fromRaw(txBytes, {
    allowUnknownOutputs: true,
  });

  if (!tx) {
    throw new Error("Failed to parse transaction");
  }
  return tx;
}

export function getTxFromRawTxBytes(rawTxBytes: Uint8Array): btc.Transaction {
  const tx = btc.Transaction.fromRaw(rawTxBytes, {
    allowUnknownOutputs: true,
  });
  if (!tx) {
    throw new Error("Failed to parse transaction");
  }
  return tx;
}

export function getSigHashFromTx(
  tx: btc.Transaction,
  inputIndex: number,
  prevOutput: TransactionOutput,
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
    new Array(tx.inputsLength).fill(amount),
  );
}

export function getTxId(tx: btc.Transaction): string {
  return bytesToHex(sha256(sha256(tx.unsignedTx)).reverse());
}

export function getTxIdNoReverse(tx: btc.Transaction): string {
  return bytesToHex(sha256(sha256(tx.unsignedTx)));
}
