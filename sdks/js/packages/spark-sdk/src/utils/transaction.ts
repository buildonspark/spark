import { Transaction } from "@scure/btc-signer";
import { TransactionInput, TransactionOutput } from "@scure/btc-signer/psbt";
import { hexToBytes } from "@noble/curves/utils";
import { SparkValidationError } from "../errors/types.js";
import { getSparkFrost } from "../spark-bindings/spark-bindings.js";
import {
  getP2TRAddressFromPkScript,
  getTxFromRawTxBytes,
  getTxId,
} from "./bitcoin.js";
import { Network } from "./network.js";

const INITIAL_TIMELOCK = 2000;
const TEST_UNILATERAL_TIMELOCK = 100;

export const TIME_LOCK_INTERVAL = 100;
export const DIRECT_TIMELOCK_OFFSET = 50;
export const HTLC_TIMELOCK_OFFSET = 70;
export const DIRECT_HTLC_TIMELOCK_OFFSET = 85;

export const INITIAL_SEQUENCE = INITIAL_TIMELOCK;

export const TEST_UNILATERAL_SEQUENCE = TEST_UNILATERAL_TIMELOCK;
export const TEST_UNILATERAL_DIRECT_SEQUENCE =
  TEST_UNILATERAL_TIMELOCK + DIRECT_TIMELOCK_OFFSET;

const INITIAL_ROOT_NODE_SEQUENCE = 0;

// Default fee constants matching Go implementation
const ESTIMATED_TX_SIZE = 191;
const DEFAULT_SATS_PER_VBYTE = 5;
export const DEFAULT_FEE_SATS = ESTIMATED_TX_SIZE * DEFAULT_SATS_PER_VBYTE;

function networkToString(network: Network): string {
  switch (network) {
    case Network.MAINNET:
      return "mainnet";
    case Network.TESTNET:
      return "testnet";
    case Network.SIGNET:
      return "signet";
    case Network.REGTEST:
    case Network.LOCAL:
      return "regtest";
  }
}

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

async function createNodeTxs({
  parentTx,
  sequence,
  directSequence,
  vout,
  network,
}: {
  parentTx: Transaction;
  sequence: number;
  directSequence?: number;
  vout: number;
  network: Network;
}): Promise<{
  nodeTx: Transaction;
  directNodeTx: Transaction;
}> {
  const parentOutput = parentTx.getOutput(vout);
  if (!parentOutput.amount || !parentOutput.script) {
    throw new SparkValidationError("Parent output amount or script not found", {
      field: "parentOutput",
      value: parentOutput,
    });
  }

  const address = getP2TRAddressFromPkScript(parentOutput.script, network);
  const actualDirectSequence =
    directSequence ?? sequence + DIRECT_TIMELOCK_OFFSET;

  const sparkFrost = getSparkFrost();
  const result = await sparkFrost.constructNodeTxPair(
    parentTx.toBytes(true),
    vout,
    address,
    sequence,
    actualDirectSequence,
    BigInt(DEFAULT_FEE_SATS),
  );

  const nodeTx = getTxFromRawTxBytes(result.cpfp.tx);
  const directNodeTx = getTxFromRawTxBytes(result.direct.tx);

  return { nodeTx, directNodeTx };
}

export async function createRootNodeTx(
  parentTx: Transaction,
  vout: number,
  network: Network,
): Promise<{
  nodeTx: Transaction;
  directNodeTx: Transaction;
}> {
  return createNodeTxs({
    parentTx,
    sequence: INITIAL_ROOT_NODE_SEQUENCE,
    vout,
    network,
  });
}

export async function createZeroTimelockNodeTx(
  parentTx: Transaction,
  network: Network,
): Promise<{
  nodeTx: Transaction;
  directNodeTx: Transaction;
}> {
  return createNodeTxs({
    parentTx,
    sequence: INITIAL_ROOT_NODE_SEQUENCE,
    directSequence: DIRECT_TIMELOCK_OFFSET,
    vout: 0,
    network,
  });
}

export async function createInitialTimelockNodeTx(
  parentTx: Transaction,
  network: Network,
) {
  return createNodeTxs({
    parentTx,
    sequence: INITIAL_SEQUENCE,
    vout: 0,
    network,
  });
}

export async function createDecrementedTimelockNodeTx(
  parentTx: Transaction,
  currentTx: Transaction,
  network: Network,
) {
  const currentSequence = currentTx.getInput(0).sequence;
  if (!currentSequence) {
    throw new SparkValidationError("Current sequence not found", {
      field: "currentSequence",
      value: currentSequence,
    });
  }

  return createNodeTxs({
    parentTx,
    sequence: getNextTransactionSequence(currentSequence).nextSequence,
    vout: 0,
    network,
  });
}

export async function createTestUnilateralTimelockNodeTx(
  parentTx: Transaction,
  nodeTx: Transaction,
  network: Network,
) {
  const sequence = nodeTx.getInput(0).sequence;
  if (!sequence) {
    throw new SparkValidationError("Sequence not found", {
      field: "sequence",
      value: sequence,
    });
  }
  const isBit30Defined = (sequence || 0) & (1 << 30);
  return createNodeTxs({
    parentTx,
    sequence: isBit30Defined | TEST_UNILATERAL_TIMELOCK,
    vout: 0,
    network,
  });
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
    throw new SparkValidationError("timelock interval is less than 0", {
      field: "nextTimelock",
      value: nextTimelock,
      expected: "Non-negative timelock interval",
    });
  } else if (!isNodeTx && nextTimelock <= 0) {
    throw new SparkValidationError(
      "timelock interval is less than or equal to 0",
      {
        field: "nextTimelock",
        value: nextTimelock,
        expected: "Timelock greater than 0",
      },
    );
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
  enforceTimelocks?: boolean;
}

interface RefundTxWithSequenceAndConnectorOutputParams extends RefundTxWithSequenceParams {
  connectorOutput: TransactionInput;
}

interface RefundTxs {
  cpfpRefundTx: Transaction;
  directRefundTx?: Transaction;
  directFromCpfpRefundTx?: Transaction;
}

async function createRefundTxs({
  nodeTx,
  directNodeTx,
  receivingPubkey,
  network,
  sequence,
  enforceTimelocks = false,
}: RefundTxWithSequenceParams): Promise<RefundTxs> {
  if (enforceTimelocks) {
    let currentTimelock = getCurrentTimelock(sequence);
    const remainder = currentTimelock % TIME_LOCK_INTERVAL;
    if (remainder !== 0) {
      currentTimelock = currentTimelock - remainder;
    }

    sequence = (currentTimelock & 0xffff) | ((sequence & (1 << 30)) >>> 0);
  }

  const directSequence = sequence + DIRECT_TIMELOCK_OFFSET;
  const isZeroNode = !getCurrentTimelock(nodeTx.getInput(0).sequence);

  const directNodeTxBytes =
    directNodeTx && !isZeroNode ? directNodeTx.toBytes(true) : null;

  const sparkFrost = getSparkFrost();
  const result = await sparkFrost.constructRefundTxTrio(
    nodeTx.toBytes(true),
    directNodeTxBytes,
    0,
    receivingPubkey,
    networkToString(network),
    sequence,
    directSequence,
    BigInt(DEFAULT_FEE_SATS),
  );

  const cpfpRefundTx = getTxFromRawTxBytes(result.cpfp_refund.tx);
  const directRefundTx = result.direct_refund
    ? getTxFromRawTxBytes(result.direct_refund.tx)
    : undefined;
  const directFromCpfpRefundTx = getTxFromRawTxBytes(
    result.direct_from_cpfp_refund.tx,
  );

  return { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx };
}

export async function createInitialTimelockRefundTxs(
  params: RefundTxParams,
): Promise<RefundTxs> {
  return createRefundTxs({
    ...params,
    sequence: INITIAL_SEQUENCE,
  });
}

export async function createDecrementedTimelockRefundTxs(
  params: RefundTxWithSequenceParams,
): Promise<RefundTxs> {
  const nextSequence = getNextTransactionSequence(params.sequence).nextSequence;

  return createRefundTxs({
    ...params,
    sequence: nextSequence,
  });
}

export async function createCurrentTimelockRefundTxs(
  params: RefundTxWithSequenceParams,
): Promise<RefundTxs> {
  return createRefundTxs({
    ...params,
    enforceTimelocks: true,
  });
}

export async function createTestUnilateralRefundTxs(
  params: RefundTxParams,
): Promise<RefundTxs> {
  return createRefundTxs({
    ...params,
    sequence: TEST_UNILATERAL_SEQUENCE,
  });
}

export async function createConnectorRefundTxs(
  params: RefundTxWithSequenceAndConnectorOutputParams,
): Promise<RefundTxs> {
  const { connectorOutput, ...baseParams } = params;
  const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
    await createDecrementedTimelockRefundTxs(baseParams);

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
    throw new SparkValidationError("Timelock not active", {
      field: "currSequence",
      value: currSequence,
    });
  }

  // Check bit 22 is active. If not equal to 0, block based time lock not active.
  const RELATIVE_TIME_LOCK_ACTIVE = (currSequence || 0) & 0x00400000;
  if (RELATIVE_TIME_LOCK_ACTIVE !== 0) {
    throw new SparkValidationError("Block based timelock not active", {
      field: "currSequence",
      value: currSequence,
    });
  }
}

export function isZeroTimelock(currSequence: number) {
  return getCurrentTimelock(currSequence) === 0;
}

// A transaction's timelock should never go below 100 blocks following a
// transfer or renewal operation, as this risks interfering with watchtowers
export function doesTxnNeedRenewed(currSequence: number) {
  const currentTimelock = getCurrentTimelock(currSequence);
  // Refresh if current timelock is less than 200 blocks.
  // This is to prevent the timelock from going below 100 blocks following a

  return currentTimelock < 200;
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
    throw new SparkValidationError("timelock interval is less than 0", {
      field: "nextTimelock",
      value: nextTimelock,
      expected: "Non-negative timelock interval",
    });
  } else if (!isNodeTx && nextTimelock <= 0) {
    throw new SparkValidationError(
      "timelock interval is less than or equal to 0",
      {
        field: "nextTimelock",
        value: nextTimelock,
        expected: "Timelock greater than 0",
      },
    );
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

// Matches Go spark.ZeroSequence — avoids bit 31 (timelock disabled flag) being set.
export const ZERO_SEQUENCE = 1 << 30;

/**
 * Creates a multi-input root transaction that consolidates multiple on-chain
 * UTXOs (all to the same deposit address) into a single root output.
 *
 * Input ordering: primary UTXO first, then additional UTXOs in array order.
 * The output uses the pkScript from the first deposit output and sums all amounts.
 */
export function createMultiInputRootTx(
  depositTxs: { tx: Transaction; vout: number }[],
): Transaction {
  if (depositTxs.length === 0) {
    throw new SparkValidationError("depositTxs must not be empty", {
      field: "depositTxs",
      value: depositTxs.length,
      expected: "At least 1 deposit transaction",
    });
  }

  const rootTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });

  let totalAmount = 0n;
  for (const { tx, vout } of depositTxs) {
    const output = tx.getOutput(vout);
    if (output.amount === undefined || !output.script) {
      throw new SparkValidationError(
        "Deposit transaction output missing amount or script",
        {
          field: "depositTxOutput",
          value: {
            vout,
            hasAmount: !!output.amount,
            hasScript: !!output.script,
          },
        },
      );
    }
    rootTx.addInput({
      txid: hexToBytes(getTxId(tx)),
      index: vout,
      sequence: ZERO_SEQUENCE,
    });
    totalAmount += output.amount;
  }

  const firstOutput = depositTxs[0]!.tx.getOutput(depositTxs[0]!.vout);
  rootTx.addOutput({
    script: firstOutput.script,
    amount: totalAmount,
  });
  rootTx.addOutput(getEphemeralAnchorOutput());

  return rootTx;
}
