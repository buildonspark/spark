import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";

import * as btc from "@scure/btc-signer";
import { TransactionOutput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { ValidationError } from "../errors/index.js";
import { getNetwork, Network } from "./network.js";

// const t = tapTweak(pubKey, h); // t = int_from_bytes(tagged_hash("TapTweak", pubkey + h)
// const P = u.lift_x(u.bytesToNumberBE(pubKey)); // P = lift_x(int_from_bytes(pubkey))
// const Q = P.add(Point.fromPrivateKey(t)); // Q = point_add(P, point_mul(G, t))
export function computeTaprootKeyNoScript(pubkey: Uint8Array): Uint8Array {
  if (pubkey.length !== 32) {
    throw new ValidationError("Public key must be 32 bytes", {
      field: "pubkey",
      value: pubkey.length,
      expected: 32,
    });
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
    throw new ValidationError("Public key must be 33 bytes", {
      field: "pubKey",
      value: pubKey.length,
      expected: 33,
    });
  }

  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const script = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    getNetwork(network),
  ).script;
  if (!script) {
    throw new ValidationError("Failed to get P2TR script", {
      field: "script",
      value: "null",
    });
  }
  return script;
}

export function getP2TRAddressFromPublicKey(
  pubKey: Uint8Array,
  network: Network,
): string {
  if (pubKey.length !== 33) {
    throw new ValidationError("Public key must be 33 bytes", {
      field: "pubKey",
      value: pubKey.length,
      expected: 33,
    });
  }

  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const address = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    getNetwork(network),
  ).address;
  if (!address) {
    throw new ValidationError("Failed to get P2TR address", {
      field: "address",
      value: "null",
    });
  }
  return address;
}

export function getP2TRAddressFromPkScript(
  pkScript: Uint8Array,
  network: Network,
): string {
  if (pkScript.length !== 34 || pkScript[0] !== 0x51 || pkScript[1] !== 0x20) {
    throw new ValidationError("Invalid pkscript", {
      field: "pkScript",
      value: bytesToHex(pkScript),
      expected: "34 bytes starting with 0x51 0x20",
    });
  }

  const parsedScript = btc.OutScript.decode(pkScript);

  return btc.Address(getNetwork(network)).encode(parsedScript);
}

export function getP2WPKHAddressFromPublicKey(
  pubKey: Uint8Array,
  network: Network,
): string {
  if (pubKey.length !== 33) {
    throw new ValidationError("Public key must be 33 bytes", {
      field: "pubKey",
      value: pubKey.length,
      expected: 33,
    });
  }

  const address = btc.p2wpkh(pubKey, getNetwork(network)).address;
  if (!address) {
    throw new ValidationError("Failed to get P2WPKH address", {
      field: "address",
      value: "null",
    });
  }
  return address;
}

export function getTxFromRawTxHex(rawTxHex: string): btc.Transaction {
  const txBytes = hexToBytes(rawTxHex);
  const tx = btc.Transaction.fromRaw(txBytes, {
    allowUnknownOutputs: true,
  });

  if (!tx) {
    throw new ValidationError("Failed to parse transaction", {
      field: "tx",
      value: "null",
    });
  }
  return tx;
}

export function getTxFromRawTxBytes(rawTxBytes: Uint8Array): btc.Transaction {
  const tx = btc.Transaction.fromRaw(rawTxBytes, {
    allowUnknownOutputs: true,
  });
  if (!tx) {
    throw new ValidationError("Failed to parse transaction", {
      field: "tx",
      value: "null",
    });
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
    throw new ValidationError("No script found in prevOutput", {
      field: "prevScript",
      value: "null",
    });
  }

  const amount = prevOutput.amount;
  if (!amount) {
    throw new ValidationError("No amount found in prevOutput", {
      field: "amount",
      value: "null",
    });
  }

  return tx.preimageWitnessV1(
    inputIndex,
    new Array(tx.inputsLength).fill(prevScript),
    btc.SigHash.DEFAULT,
    new Array(tx.inputsLength).fill(amount),
  );
}

export function getTxId(tx: btc.Transaction): string {
  return bytesToHex(sha256(sha256(tx.toBytes(true))).reverse());
}

export function getTxIdNoReverse(tx: btc.Transaction): string {
  return bytesToHex(sha256(sha256(tx.toBytes(true))));
}
