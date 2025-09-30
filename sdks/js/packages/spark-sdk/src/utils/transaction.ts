import { hexToBytes } from "@noble/hashes/utils";
import { Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { ValidationError } from "../errors/types.js";
import { getP2TRScriptFromPublicKey, getTxId } from "./bitcoin.js";
import { Network } from "./network.js";

const INITIAL_TIMELOCK = 2000;
const TEST_UNILATERAL_TIMELOCK = 100;

const TIME_LOCK_INTERVAL = 100;
export const DIRECT_TIMELOCK_OFFSET = 50;
export const HTLC_TIMELOCK_OFFSET = 70;
export const DIRECT_HTLC_TIMELOCK_OFFSET = 85;

export const INITIAL_SEQUENCE = (1 << 30) | INITIAL_TIMELOCK;

export const TEST_UNILATERAL_SEQUENCE = (1 << 30) | TEST_UNILATERAL_TIMELOCK;
export const TEST_UNILATERAL_DIRECT_SEQUENCE =
  (1 << 30) | (TEST_UNILATERAL_TIMELOCK + DIRECT_TIMELOCK_OFFSET);

const INITIAL_ROOT_NODE_SEQUENCE = (1 << 30) | 0;

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

interface CreateNodeTxInput {
  sequence: number;
  txOut: TransactionOutput;
  parentOutPoint: TransactionInput;
  applyFee?: boolean;
  includeAnchor?: boolean;
}
// createNodeTx creates a node transaction.
// This stands in between a split tx and a leaf node tx,
// and has no timelock.
function createNodeTx({
  sequence,
  txOut,
  parentOutPoint,
  applyFee,
  includeAnchor,
}: CreateNodeTxInput): Transaction {
  const nodeTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  nodeTx.addInput({
    ...parentOutPoint,
    sequence,
  });

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

function createNodeTxs({
  parentTx,
  sequence,
  directSequence,
  vout,
}: {
  parentTx: Transaction;
  sequence: number;
  directSequence?: number;
  vout: number;
}): {
  nodeTx: Transaction;
  directNodeTx: Transaction;
} {
  const parentOutput = parentTx.getOutput(vout);
  if (!parentOutput.amount || !parentOutput.script) {
    throw new ValidationError("Parent output amount or script not found", {
      field: "parentOutput",
      value: parentOutput,
    });
  }
  const output = {
    script: parentOutput.script,
    amount: parentOutput.amount,
  };

  const input = {
    txid: hexToBytes(getTxId(parentTx)!),
    index: vout,
  };

  const nodeTx = createNodeTx({
    sequence,
    txOut: output,
    parentOutPoint: input,
    includeAnchor: true,
  });

  const directNodeTx = createNodeTx({
    sequence: directSequence ?? sequence + DIRECT_TIMELOCK_OFFSET,
    txOut: output,
    parentOutPoint: input,
    includeAnchor: false,
    applyFee: true,
  });

  return { nodeTx, directNodeTx };
}

export function createRootNodeTx(
  parentTx: Transaction,
  vout: number,
): {
  nodeTx: Transaction;
  directNodeTx: Transaction;
} {
  return createNodeTxs({
    parentTx,
    sequence: INITIAL_ROOT_NODE_SEQUENCE,
    vout,
  });
}

export function createZeroTimelockNodeTx(parentTx: Transaction): {
  nodeTx: Transaction;
  directNodeTx: Transaction;
} {
  return createNodeTxs({
    parentTx,
    sequence: INITIAL_ROOT_NODE_SEQUENCE,
    directSequence: DIRECT_TIMELOCK_OFFSET,
    vout: 0,
  });
}

export function createInitialTimelockNodeTx(parentTx: Transaction) {
  return createNodeTxs({
    parentTx,
    sequence: INITIAL_SEQUENCE,
    vout: 0,
  });
}

export function createDecrementedTimelockNodeTx(
  parentTx: Transaction,
  currentTx: Transaction,
) {
  const currentSequence = currentTx.getInput(0).sequence;
  if (!currentSequence) {
    throw new ValidationError("Current sequence not found", {
      field: "currentSequence",
      value: currentSequence,
    });
  }

  return createNodeTxs({
    parentTx,
    sequence: getNextTransactionSequence(currentSequence).nextSequence,
    vout: 0,
  });
}

export function createTestUnilateralTimelockNodeTx(
  parentTx: Transaction,
  nodeTx: Transaction,
) {
  const sequence = nodeTx.getInput(0).sequence;
  if (!sequence) {
    throw new ValidationError("Sequence not found", {
      field: "sequence",
      value: sequence,
    });
  }
  const isBit30Defined = (sequence || 0) & (1 << 30);
  return createNodeTxs({
    parentTx,
    sequence: isBit30Defined | TEST_UNILATERAL_TIMELOCK,
    vout: 0,
  });
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
function createRefundTx({
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

export function getNextHTLCTransactionSequence(
  currSequence: number,
  isNodeTx?: boolean,
): {
  nextSequence: number;
  nextDirectSequence: number;
} {
  const currentTimelock = getCurrentTimelock(currSequence);
  const nextTimelock = currentTimelock - TIME_LOCK_INTERVAL;
  const isBit30Defined = (currSequence || 0) & (1 << 30);

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

  // If bit 30 is defined, we need to add it to the next sequence.
  return {
    nextSequence: isBit30Defined | (nextTimelock + HTLC_TIMELOCK_OFFSET),
    nextDirectSequence:
      isBit30Defined | (nextTimelock + DIRECT_HTLC_TIMELOCK_OFFSET),
  };
}
interface RefundTxParams {
  nodeTx: Transaction;
  directNodeTx?: Transaction;
  receivingPubkey: Uint8Array;
  network: Network;
}

interface RefundTxWithSequenceParams extends RefundTxParams {
  sequence: number;
}

interface RefundTxWithSequenceAndConnectorOutputParams
  extends RefundTxWithSequenceParams {
  connectorOutput: TransactionInput;
}

interface RefundTxs {
  cpfpRefundTx: Transaction;
  directRefundTx?: Transaction;
  directFromCpfpRefundTx?: Transaction;
}

function createRefundTxs({
  nodeTx,
  directNodeTx,
  receivingPubkey,
  network,
  sequence,
}: RefundTxWithSequenceParams): RefundTxs {
  const refundInput: TransactionInput = {
    txid: hexToBytes(getTxId(nodeTx)!),
    index: 0,
  };

  const nodeAmountSats = nodeTx.getOutput(0).amount;
  if (nodeAmountSats === undefined) {
    throw new ValidationError("Node amount not found", {
      field: "nodeAmountSats",
      value: nodeAmountSats,
    });
  }

  let directRefundTx: Transaction | undefined;
  if (directNodeTx) {
    const directRefundInput: TransactionInput = {
      txid: hexToBytes(getTxId(directNodeTx)),
      index: 0,
    };

    const directAmountSats = directNodeTx.getOutput(0).amount;
    if (directAmountSats === undefined) {
      throw new ValidationError("Direct amount not found", {
        field: "directAmountSats",
        value: directAmountSats,
      });
    }

    directRefundTx = createRefundTx({
      sequence: sequence + DIRECT_TIMELOCK_OFFSET,
      input: directRefundInput,
      amountSats: directAmountSats,
      receivingPubkey,
      network,
      shouldCalculateFee: true,
      includeAnchor: false,
    });
  }

  const cpfpRefundTx = createRefundTx({
    sequence: sequence,
    input: refundInput,
    amountSats: nodeAmountSats,
    receivingPubkey,
    network,
    shouldCalculateFee: false,
    includeAnchor: true,
  });

  const directFromCpfpRefundTx = createRefundTx({
    sequence: sequence + DIRECT_TIMELOCK_OFFSET,
    input: refundInput,
    amountSats: nodeAmountSats,
    receivingPubkey,
    network,
    shouldCalculateFee: true,
    includeAnchor: false,
  });

  return { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx };
}

export function createInitialTimelockRefundTxs(
  params: RefundTxParams,
): RefundTxs {
  return createRefundTxs({
    ...params,
    sequence: INITIAL_SEQUENCE,
  });
}

export function createDecrementedTimelockRefundTxs(
  params: RefundTxWithSequenceParams,
): RefundTxs {
  const nextSequence = getNextTransactionSequence(params.sequence).nextSequence;

  return createRefundTxs({
    ...params,
    sequence: nextSequence,
  });
}

export function createCurrentTimelockRefundTxs(
  params: RefundTxWithSequenceParams,
): RefundTxs {
  return createRefundTxs(params);
}

export function createTestUnilateralRefundTxs(
  params: RefundTxParams,
): RefundTxs {
  return createRefundTxs({
    ...params,
    sequence: TEST_UNILATERAL_SEQUENCE,
  });
}

export function createConnectorRefundTxs(
  params: RefundTxWithSequenceAndConnectorOutputParams,
): RefundTxs {
  const { connectorOutput, ...baseParams } = params;
  const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
    createDecrementedTimelockRefundTxs(baseParams);

  cpfpRefundTx.addInput(connectorOutput);
  if (directRefundTx) {
    directRefundTx.addInput(connectorOutput);
  }
  if (directFromCpfpRefundTx) {
    directFromCpfpRefundTx.addInput(connectorOutput);
  }

  return { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx };
}

export function getCurrentTimelock(currSequence?: number): number {
  return (currSequence || 0) & 0xffff;
}

export function getTransactionSequence(currSequence?: number): {
  nextSequence: number;
  nextDirectSequence: number;
} {
  const timelock = getCurrentTimelock(currSequence);
  const isBit30Defined = (currSequence || 0) & (1 << 30);

  return {
    nextSequence: isBit30Defined | timelock,
    nextDirectSequence: isBit30Defined | (timelock + DIRECT_TIMELOCK_OFFSET),
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

export function isZeroTimelock(currSequence: number) {
  return getCurrentTimelock(currSequence) === 0;
}

export function doesTxnNeedRenewed(currSequence: number) {
  const currentTimelock = getCurrentTimelock(currSequence);
  return currentTimelock <= 100;
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
  const isBit30Defined = (currSequence || 0) & (1 << 30);

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
    nextSequence: isBit30Defined | nextTimelock,
    nextDirectSequence:
      isBit30Defined | (nextTimelock + DIRECT_TIMELOCK_OFFSET),
  };
}

export function getEphemeralAnchorOutput(): TransactionOutput {
  return {
    script: new Uint8Array([0x51, 0x02, 0x4e, 0x73]), // Pay-to-anchor (P2A) ephemeral anchor output
    amount: 0n,
  };
}
