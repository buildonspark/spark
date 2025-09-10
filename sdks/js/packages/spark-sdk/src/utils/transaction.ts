import { Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { ValidationError } from "../errors/types.js";
import { getP2TRScriptFromPublicKey } from "./bitcoin.js";
import { Network } from "./network.js";

const INITIAL_TIMELOCK = 2000;
const TEST_UNILATERAL_TIMELOCK = 100;

const TIME_LOCK_INTERVAL = 100;
export const DIRECT_TIMELOCK_OFFSET = 50;

export const INITIAL_SEQUENCE = (1 << 30) | INITIAL_TIMELOCK;
export const INITIAL_DIRECT_SEQUENCE =
  (1 << 30) | (INITIAL_TIMELOCK + DIRECT_TIMELOCK_OFFSET);
export const TEST_UNILATERAL_SEQUENCE = (1 << 30) | TEST_UNILATERAL_TIMELOCK;
export const TEST_UNILATERAL_DIRECT_SEQUENCE =
  (1 << 30) | (TEST_UNILATERAL_TIMELOCK + DIRECT_TIMELOCK_OFFSET);

// Default fee constants matching Go implementation
const ESTIMATED_TX_SIZE = 191;
const DEFAULT_SATS_PER_VBYTE = 5;
export const DEFAULT_FEE_SATS = ESTIMATED_TX_SIZE * DEFAULT_SATS_PER_VBYTE;

/**
 * Subtracts the default fee from the amount if it's greater than the fee.
 * Returns the original amount if it's less than or equal to the fee.
 */
export function maybeApplyFee(amount: bigint): bigint {
  if (amount > BigInt(DEFAULT_FEE_SATS)) {
    return amount - BigInt(DEFAULT_FEE_SATS);
  }
  return amount;
}

export function createRootTx(
  depositOutPoint: TransactionInput,
  depositTxOut: TransactionOutput,
): [Transaction, Transaction] {
  // Create CPFP-friendly root tx (with ephemeral anchor, no fee)
  const cpfpRootTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  cpfpRootTx.addInput(depositOutPoint);
  cpfpRootTx.addOutput(depositTxOut);
  cpfpRootTx.addOutput(getEphemeralAnchorOutput());

  // Create direct root tx (with fee, no anchor)
  const directRootTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  directRootTx.addInput(depositOutPoint);
  directRootTx.addOutput({
    script: depositTxOut.script,
    amount: maybeApplyFee(depositTxOut.amount ?? 0n),
  });

  return [cpfpRootTx, directRootTx];
}

export function createSplitTx(
  parentOutPoint: TransactionInput,
  childTxOuts: TransactionOutput[],
): [Transaction, Transaction] {
  // Create CPFP-friendly split tx (with ephemeral anchor, no fee)
  const cpfpSplitTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  cpfpSplitTx.addInput(parentOutPoint);
  for (const txOut of childTxOuts) {
    cpfpSplitTx.addOutput(txOut);
  }
  cpfpSplitTx.addOutput(getEphemeralAnchorOutput());

  // Create direct split tx (with fee, no anchor)
  const directSplitTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  directSplitTx.addInput(parentOutPoint);

  // Adjust output amounts to account for fee
  let totalOutputAmount = 0n;
  for (const txOut of childTxOuts) {
    totalOutputAmount += txOut.amount ?? 0n;
  }

  if (totalOutputAmount > BigInt(DEFAULT_FEE_SATS)) {
    // Distribute fee proportionally across outputs
    const feeRatio = Number(DEFAULT_FEE_SATS) / Number(totalOutputAmount);
    for (const txOut of childTxOuts) {
      const adjustedAmount = BigInt(
        Math.floor(Number(txOut.amount ?? 0n) * (1 - feeRatio)),
      );
      directSplitTx.addOutput({
        script: txOut.script,
        amount: adjustedAmount,
      });
    }
  } else {
    // If fee is larger than total output, just pass through original amounts
    for (const txOut of childTxOuts) {
      directSplitTx.addOutput(txOut);
    }
  }

  return [cpfpSplitTx, directSplitTx];
}

interface CreateNodeTxInput {
  txOut: TransactionOutput;
  parentOutPoint: TransactionInput;
  applyFee?: boolean;
  includeAnchor?: boolean;
}
// createNodeTx creates a node transaction.
// This stands in between a split tx and a leaf node tx,
// and has no timelock.
export function createNodeTx({
  txOut,
  parentOutPoint,
  applyFee,
  includeAnchor,
}: CreateNodeTxInput): Transaction {
  const nodeTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  nodeTx.addInput(parentOutPoint);

  if (applyFee) {
    nodeTx.addOutput({
      script: txOut.script,
      amount: maybeApplyFee(txOut.amount ?? 0n),
    });
  } else {
    nodeTx.addOutput(txOut);
  }

  if (includeAnchor) {
    nodeTx.addOutput(getEphemeralAnchorOutput());
  }

  return nodeTx;
}

export function createNodeTxs(
  txOut: TransactionOutput,
  txIn: TransactionInput,
  directTxIn?: TransactionInput,
): {
  cpfpNodeTx: Transaction;
  directNodeTx?: Transaction;
} {
  const cpfpNodeTx = createNodeTx({
    txOut,
    parentOutPoint: txIn,
    includeAnchor: true,
  });

  let directNodeTx: Transaction | undefined;
  if (directTxIn) {
    directNodeTx = createNodeTx({
      txOut,
      parentOutPoint: directTxIn,
      includeAnchor: false,
      applyFee: true,
    });
  }

  return { cpfpNodeTx, directNodeTx };
}

// createLeafNodeTx creates a leaf node transaction.
// This transaction provides an intermediate transaction
// to allow the timelock of the final refund transaction
// to be extended. E.g. when the refund tx timelock reaches
// 0, the leaf node tx can be re-signed with a decremented
// timelock, and the refund tx can be reset it's timelock.
export function createLeafNodeTx(
  sequence: number,
  directSequence: number,
  parentOutPoint: TransactionInput,
  txOut: TransactionOutput,
  shouldCalculateFee: boolean,
): [Transaction, Transaction] {
  // Create CPFP-friendly leaf node tx (with ephemeral anchor, no fee)
  const cpfpLeafTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  cpfpLeafTx.addInput({
    ...parentOutPoint,
    sequence,
  });
  cpfpLeafTx.addOutput(txOut);
  cpfpLeafTx.addOutput(getEphemeralAnchorOutput());

  // Create direct leaf node tx (with fee, no anchor)
  const directLeafTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  directLeafTx.addInput({
    ...parentOutPoint,
    sequence: directSequence,
  });
  const amountSats = txOut.amount ?? 0n;
  let outputAmount = amountSats;
  if (shouldCalculateFee) {
    outputAmount = maybeApplyFee(amountSats);
  }
  directLeafTx.addOutput({
    script: txOut.script,
    amount: outputAmount,
  });

  return [cpfpLeafTx, directLeafTx];
}

interface CreateRefundTxInput {
  sequence: number;
  input: TransactionInput;
  amountSats: bigint;
  receivingPubkey: Uint8Array;
  network: Network;
  shouldCalculateFee: boolean;
  includeAnchor: boolean;
}
export function createRefundTx({
  sequence,
  input,
  amountSats,
  receivingPubkey,
  network,
  shouldCalculateFee,
  includeAnchor,
}: CreateRefundTxInput): Transaction {
  const refundTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  refundTx.addInput({
    ...input,
    sequence,
  });

  const refundPkScript = getP2TRScriptFromPublicKey(receivingPubkey, network);

  let outputAmount = amountSats;
  if (shouldCalculateFee) {
    outputAmount = maybeApplyFee(amountSats);
  }

  refundTx.addOutput({
    script: refundPkScript,
    amount: outputAmount,
  });

  if (includeAnchor) {
    refundTx.addOutput(getEphemeralAnchorOutput());
  }

  return refundTx;
}

interface CreateRefundTxsInput {
  sequence: number;
  directSequence?: number;
  input: TransactionInput;
  directInput?: TransactionInput;
  amountSats: bigint;
  receivingPubkey: Uint8Array;
  network: Network;
}
export function createRefundTxs({
  sequence,
  directSequence,
  input,
  directInput,
  amountSats,
  receivingPubkey,
  network,
}: CreateRefundTxsInput): {
  cpfpRefundTx: Transaction;
  directRefundTx?: Transaction;
  directFromCpfpRefundTx?: Transaction;
} {
  const cpfpRefundTx = createRefundTx({
    sequence,
    input,
    amountSats,
    receivingPubkey,
    network,
    shouldCalculateFee: false,
    includeAnchor: true,
  });

  let directRefundTx: Transaction | undefined;
  let directFromCpfpRefundTx: Transaction | undefined;
  if (directSequence && directInput) {
    directRefundTx = createRefundTx({
      sequence: directSequence,
      input: directInput,
      amountSats,
      receivingPubkey,
      network,
      shouldCalculateFee: true,
      includeAnchor: false,
    });
    directFromCpfpRefundTx = createRefundTx({
      sequence: directSequence,
      input,
      amountSats,
      receivingPubkey,
      network,
      shouldCalculateFee: true,
      includeAnchor: false,
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

export function createConnectorRefundTransactions(
  sequence: number,
  cpfpNodeOutPoint: TransactionInput,
  directNodeOutPoint: TransactionInput,
  connectorOutput: TransactionInput,
  amountSats: bigint,
  receiverPubKey: Uint8Array,
  network: Network,
  shouldCalculateFee: boolean,
): [Transaction, Transaction, Transaction] {
  // Create CPFP-friendly connector refund tx (with ephemeral anchor, no fee)
  const cpfpRefundTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  cpfpRefundTx.addInput({
    ...cpfpNodeOutPoint,
    sequence,
  });
  cpfpRefundTx.addInput(connectorOutput);
  const receiverScript = getP2TRScriptFromPublicKey(receiverPubKey, network);
  cpfpRefundTx.addOutput({
    script: receiverScript,
    amount: amountSats,
  });

  // Create direct connector refund tx (with fee, no anchor)
  const directRefundTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  directRefundTx.addInput({
    ...directNodeOutPoint,
    sequence,
  });
  directRefundTx.addInput(connectorOutput);

  let outputAmount = amountSats;
  if (shouldCalculateFee) {
    outputAmount = maybeApplyFee(amountSats);
  }
  directRefundTx.addOutput({
    script: receiverScript,
    amount: outputAmount,
  });

  // Create direct-style refund tx that spends from CPFP outpoint (with fee, no anchor)
  const directFromCpfpTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  directFromCpfpTx.addInput({
    ...cpfpNodeOutPoint,
    sequence,
  });
  directFromCpfpTx.addInput(connectorOutput);
  directFromCpfpTx.addOutput({
    script: receiverScript,
    amount: outputAmount,
  });

  return [cpfpRefundTx, directRefundTx, directFromCpfpTx];
}

export function getCurrentTimelock(currSequence?: number): number {
  return (currSequence || 0) & 0xffff;
}

export function getTransactionSequence(currSequence?: number): {
  nextSequence: number;
  nextDirectSequence: number;
} {
  const timelock = getCurrentTimelock(currSequence);
  return {
    nextSequence: (1 << 30) | timelock,
    nextDirectSequence: (1 << 30) | (timelock + DIRECT_TIMELOCK_OFFSET),
  };
}

export function checkIfValidSequence(currSequence?: number) {
  // Check bit 31 is active. If not equal to 0, timelock is not active.
  const TIME_LOCK_ACTIVE = (currSequence || 0) & 0x80000000;
  if (TIME_LOCK_ACTIVE !== 0) {
    throw new ValidationError("Timelock not active", {
      field: "currSequence",
      value: currSequence,
    });
  }

  // Check bit 22 is active. If not equal to 0, block based time lock not active.
  const RELATIVE_TIME_LOCK_ACTIVE = (currSequence || 0) & 0x00400000;
  if (RELATIVE_TIME_LOCK_ACTIVE !== 0) {
    throw new ValidationError("Block based timelock not active", {
      field: "currSequence",
      value: currSequence,
    });
  }
}

export function doesLeafNeedRefresh(currSequence: number, isNodeTx?: boolean) {
  const currentTimelock = getCurrentTimelock(currSequence);

  if (isNodeTx) {
    return currentTimelock === 0;
  }
  return currentTimelock <= 100;
}

// make sure that the leaves are ok before sending or else next user could lose funds
export function getNextTransactionSequence(
  currSequence: number,
  isNodeTx?: boolean,
): {
  nextSequence: number;
  nextDirectSequence: number;
} {
  const currentTimelock = getCurrentTimelock(currSequence);
  const nextTimelock = currentTimelock - TIME_LOCK_INTERVAL;

  if (isNodeTx && nextTimelock < 0) {
    throw new ValidationError("timelock interval is less than 0", {
      field: "nextTimelock",
      value: nextTimelock,
      expected: "Non-negative timelock interval",
    });
  } else if (!isNodeTx && nextTimelock <= 0) {
    throw new ValidationError("timelock interval is less than or equal to 0", {
      field: "nextTimelock",
      value: nextTimelock,
      expected: "Timelock greater than 0",
    });
  }

  return {
    nextSequence: (1 << 30) | nextTimelock,
    nextDirectSequence: (1 << 30) | (nextTimelock + DIRECT_TIMELOCK_OFFSET),
  };
}

export function getEphemeralAnchorOutput(): TransactionOutput {
  return {
    script: new Uint8Array([0x51, 0x02, 0x4e, 0x73]), // Pay-to-anchor (P2A) ephemeral anchor output
    amount: 0n,
  };
}
