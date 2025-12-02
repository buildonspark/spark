import {
  Transaction,
  Script,
  taprootListToTree,
  p2tr,
  ScriptNum,
  SigHash,
} from "@scure/btc-signer";
import { secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes, equalBytes } from "@noble/curves/utils";
import { BTC_NETWORK } from "@scure/btc-signer/utils";
import { TaprootControlBlock, TransactionInput } from "@scure/btc-signer/psbt";

import { maybeApplyFee, getEphemeralAnchorOutput } from "./transaction.js";
import { getTxId } from "../utils/bitcoin.js";
import { tapLeafHash } from "@scure/btc-signer/payment";
import { SparkValidationError } from "../errors/types.js";

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
    throw new SparkValidationError(
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
  const seqOperand =
    sequence >= 0 && sequence <= 16
      ? sequence // emits OP_N (matches Go)
      : ScriptNum().encode(BigInt(sequence));
  const result = Script.encode([
    seqOperand,
    "CHECKSEQUENCEVERIFY",
    "DROP",
    sequenceLockDestinationPubkey.slice(1, 33),
    "CHECKSIG",
  ]);
  return result;
}

export function createSenderSpendTx({
  htlcTx,
  network,
  hash,
  hashLockDestinationPubkey,
  sequenceLockDestinationPubkey,
  fee,
}: {
  htlcTx: Transaction;
  network: BTC_NETWORK;
  hash: Uint8Array;
  hashLockDestinationPubkey: Uint8Array;
  sequenceLockDestinationPubkey: Uint8Array;
  fee: number;
}): {
  senderSpendTx: Transaction;
  sighash: Uint8Array;
  sequenceLockScript: Uint8Array;
  controlBlockBytes: Uint8Array;
} {
  const numsKey = numsPoint();

  const senderSpendTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
    allowUnknownInputs: true,
  });

  senderSpendTx.addInput({
    txid: hexToBytes(getTxId(htlcTx)),
    index: 0,
    sequence: lightningHTLCSequence,
  });

  const senderP2TR = p2tr(
    sequenceLockDestinationPubkey.slice(1, 33),
    undefined,
    network,
    true,
  ).script;

  // check if fee is greater than the amount
  const amount = htlcTx.getOutput(0)?.amount! - BigInt(fee);

  if (amount <= 0n) {
    throw new SparkValidationError("Fee is greater than the amount", {
      field: "fee",
      value: fee,
    });
  }

  senderSpendTx.addOutput({
    script: senderP2TR,
    amount: amount,
  });

  const hashLockScript = createHashLockScript(hash, hashLockDestinationPubkey);
  const sequenceLockScript = createSequenceLockScript(
    lightningHTLCSequence,
    sequenceLockDestinationPubkey,
  );

  const hashLockLeaf = { leafVersion: 0xc0, script: hashLockScript };
  const sequenceLockLeaf = { leafVersion: 0xc0, script: sequenceLockScript };

  const scriptTree = taprootListToTree([hashLockLeaf, sequenceLockLeaf]);

  const p2 = p2tr(numsKey, scriptTree, network, true);

  const entry = p2.tapLeafScript!.find(
    ([, scriptWithVer]) =>
      Buffer.compare(scriptWithVer.slice(0, -1), sequenceLockScript) === 0,
  )!;
  const controlBlockBytes = TaprootControlBlock.encode(entry[0]);

  const seqLeafEntry = p2.tapLeafScript!.find(([_, leaf]) => {
    const script = leaf.subarray(0, leaf.length - 1); // strip leaf version
    return equalBytes(script, sequenceLockScript);
  });

  const sighash = senderSpendTx.preimageWitnessV1(
    0,
    [sequenceLockScript],
    SigHash.DEFAULT,
    [htlcTx.getOutput(0)?.amount!],
  );

  senderSpendTx.updateInput(0, {
    witnessUtxo: {
      script: p2.script,
      amount: htlcTx.getOutput(0)?.amount!,
    },
    tapLeafScript: [seqLeafEntry!],
  });

  return { senderSpendTx, sighash, sequenceLockScript, controlBlockBytes };
}

export function createReceiverSpendTx({
  htlcTx,
  network,
  hash,
  hashLockDestinationPubkey,
  sequenceLockDestinationPubkey,
  fee,
}: {
  htlcTx: Transaction;
  network: BTC_NETWORK;
  hash: Uint8Array;
  hashLockDestinationPubkey: Uint8Array;
  sequenceLockDestinationPubkey: Uint8Array;
  fee: number;
}): {
  spendTx: Transaction;
  controlBlockBytes: Uint8Array;
  leafHash: Uint8Array;
  hashLockScript: Uint8Array;
} {
  const numsKey = numsPoint();

  const spendTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
    allowUnknownInputs: true,
  });

  spendTx.addInput({
    txid: hexToBytes(getTxId(htlcTx)),
    index: 0,
  });

  const senderP2TR = p2tr(
    sequenceLockDestinationPubkey.slice(1, 33),
    undefined,
    network,
    true,
  ).script;

  const amount = htlcTx.getOutput(0)?.amount! - BigInt(fee);
  if (amount <= 0n) {
    throw new SparkValidationError("Fee is greater than the amount", {
      field: "fee",
      value: fee,
    });
  }

  spendTx.addOutput({
    script: senderP2TR,
    amount: amount,
  });

  const hashLockScript = createHashLockScript(hash, hashLockDestinationPubkey);
  const sequenceLockScript = createSequenceLockScript(
    lightningHTLCSequence,
    sequenceLockDestinationPubkey,
  );

  const hashLockLeaf = { leafVersion: 0xc0, script: hashLockScript };
  const sequenceLockLeaf = { leafVersion: 0xc0, script: sequenceLockScript };

  const scriptTree = taprootListToTree([hashLockLeaf, sequenceLockLeaf]);

  const p2 = p2tr(numsKey, scriptTree, network, true);

  const hashLeafEntry = p2.tapLeafScript!.find(([_, leaf]) => {
    const script = leaf.subarray(0, leaf.length - 1); // strip leaf version
    return equalBytes(script, hashLockScript);
  });

  spendTx.updateInput(0, {
    witnessUtxo: {
      script: p2.script,
      amount: htlcTx.getOutput(0)?.amount!,
    },
    tapLeafScript: [hashLeafEntry!],
  });

  const controlBlockBytes = TaprootControlBlock.encode(hashLeafEntry![0]);
  const leafHash = tapLeafHash(hashLockScript, 0xc0);

  return { spendTx, controlBlockBytes, leafHash, hashLockScript };
}
