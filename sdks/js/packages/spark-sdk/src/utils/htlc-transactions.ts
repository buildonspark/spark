import {
  Transaction,
  Script,
  taprootListToTree,
  p2tr,
  ScriptNum,
} from "@scure/btc-signer";
import { secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes } from "@noble/curves/utils";
import { BTC_NETWORK } from "@scure/btc-signer/utils";
import { TransactionInput } from "@scure/btc-signer/psbt";

import { maybeApplyFee, getEphemeralAnchorOutput } from "./transaction.js";
import { getTxId } from "../utils/bitcoin.js";
import { ValidationError } from "../errors/types.js";

interface CreateLightningRefundTxsInput {
  nodeTx: Transaction;
  directNodeTx: Transaction | undefined;
  vout: number;
  sequence: number;
  directSequence: number;
  directInput?: TransactionInput;
  network: BTC_NETWORK;
  hash: Uint8Array;
  hashLockDestinationPubkey: Uint8Array;
  sequenceLockDestinationPubkey: Uint8Array;
}

// Fixed BIP341 “NUMS” x-only public key (a well-known constant, not tied to any secret).
// Used as the Taproot internal key so HTLC outputs depend only on the script, not a private key.
const PUB_KEY_BYTES =
  "0250929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0";

export function numsPoint(): Buffer {
  const withdrawalPubKeyPoint = secp256k1.Point.fromHex(PUB_KEY_BYTES); // validate/parse
  const withdrawalPubKey = withdrawalPubKeyPoint.toBytes(true).slice(1);
  return Buffer.from(withdrawalPubKey);
}

const lightningHTLCSequence = 2160;

export function createRefundTxsForLightning({
  nodeTx,
  directNodeTx,
  vout,
  sequence,
  directSequence,
  directInput,
  network,
  hash,
  hashLockDestinationPubkey,
  sequenceLockDestinationPubkey,
}: CreateLightningRefundTxsInput): {
  cpfpRefundTx: Transaction;
  directRefundTx?: Transaction;
  directFromCpfpRefundTx?: Transaction;
} {
  const cpfpRefundTx = createLightningHTLCTransaction({
    nodeTx,
    sequence,
    vout,
    hash,
    hashLockDestinationPubkey,
    sequenceLockDestinationPubkey,
    applyFee: false,
    network,
  });

  const directFromCpfpRefundTx = createLightningHTLCTransaction({
    nodeTx,
    sequence: directSequence,
    vout,
    hash,
    hashLockDestinationPubkey,
    sequenceLockDestinationPubkey,
    applyFee: true,
    network,
  });

  let directRefundTx: Transaction | undefined;
  if (directSequence && directNodeTx) {
    directRefundTx = createLightningHTLCTransaction({
      nodeTx: directNodeTx,
      sequence: directSequence,
      vout,
      hash,
      hashLockDestinationPubkey,
      sequenceLockDestinationPubkey,
      applyFee: true,
      network,
    });
  } else if (directInput && !directSequence) {
    throw new ValidationError(
      "directSequence must be provided if directInput is",
      {
        field: "directSequence",
        value: directSequence,
      },
    );
  }

  return { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx };
}

// Create HTLC transaction for lightning
export function createLightningHTLCTransaction({
  nodeTx,
  vout,
  sequence,
  hash,
  hashLockDestinationPubkey,
  sequenceLockDestinationPubkey,
  applyFee,
  network,
}: {
  nodeTx: Transaction;
  vout: number;
  hash: Uint8Array;
  hashLockDestinationPubkey: Uint8Array;
  sequenceLockDestinationPubkey: Uint8Array;
  sequence: number;
  applyFee: boolean;
  network: BTC_NETWORK;
}): Transaction {
  let outAmount = nodeTx.getOutput(vout)?.amount ?? 0n;
  if (applyFee) {
    outAmount = maybeApplyFee(outAmount);
  }

  const input: TransactionInput = {
    txid: hexToBytes(getTxId(nodeTx)),
    index: 0,
  };

  const htlcTransaction = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });

  htlcTransaction.addInput({
    ...input,
    sequence,
  });

  const taprootAddress = createHTLCTaprootAddress({
    hash,
    hashLockDestinationPubkey,
    sequence: lightningHTLCSequence,
    sequenceLockDestinationPubkey,
    network,
  });

  htlcTransaction.addOutput({
    script: taprootAddress,
    amount: outAmount,
  });

  if (!applyFee) {
    // Add ephemeral anchor output
    htlcTransaction.addOutput(getEphemeralAnchorOutput());
  }

  return htlcTransaction;
}

function createHTLCTaprootAddress({
  hash,
  hashLockDestinationPubkey,
  sequence,
  sequenceLockDestinationPubkey,
  network,
}: {
  hash: Uint8Array;
  hashLockDestinationPubkey: Uint8Array;
  sequence: number;
  sequenceLockDestinationPubkey: Uint8Array;
  network: BTC_NETWORK;
}): Uint8Array {
  const numsKey = numsPoint();

  const hashLockScript = createHashLockScript(hash, hashLockDestinationPubkey);
  const sequenceLockScript = createSequenceLockScript(
    sequence,
    sequenceLockDestinationPubkey,
  );

  const hashLockLeaf = { leafVersion: 0xc0, script: hashLockScript };
  const sequenceLockLeaf = { leafVersion: 0xc0, script: sequenceLockScript };

  const scriptTree = taprootListToTree([hashLockLeaf, sequenceLockLeaf]);

  const p2trScript = p2tr(numsKey, scriptTree, network, true).script;

  return p2trScript;
}

function createHashLockScript(
  hash: Uint8Array,
  pubkey: Uint8Array,
): Uint8Array {
  const result = Script.encode([
    "SHA256",
    hash,
    "EQUALVERIFY",
    pubkey.slice(1, 33),
    "CHECKSIG",
  ]);
  return result;
}

function createSequenceLockScript(
  sequence: number,
  sequenceLockDestinationPubkey: Uint8Array,
): Uint8Array {
  const result = Script.encode([
    ScriptNum().encode(BigInt(sequence)),
    "CHECKSEQUENCEVERIFY",
    "DROP",
    sequenceLockDestinationPubkey.slice(1, 33),
    "CHECKSIG",
  ]);
  return result;
}
