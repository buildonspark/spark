import { resolveWallet } from "../wallet.js";
import {
  formatSats,
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

export async function handleGetDepositAddress(
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const address = await wallet.getSingleUseDepositAddress();
    if (output === "raw") return rawResult({ depositAddress: address });
    return {
      content: [
        {
          type: "text",
          text: `Deposit address: ${address}\nSend Bitcoin to this address to fund your Spark wallet.\nIMPORTANT: This address is single-use. After funding and claiming, request a new deposit address for the next deposit.`,
        },
      ],
    };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

export async function handleClaimDeposit(
  txid: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  settleTimeoutMs: number = 30_000,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);

    // Snapshot balance before claiming so we can detect when it settles.
    const { balance: priorBalance } = await wallet.getBalance();

    const leaves = await wallet.claimDeposit(txid);
    const claimedSats = leaves.reduce(
      (sum: number, l: { value: number }) => sum + l.value,
      0,
    );
    const expectedBalance = priorBalance + BigInt(claimedSats);

    // Poll until the balance reflects the deposit (operator needs time to
    // process new leaves). Same pattern used by the combined spark_deposit tool.
    const deadline = Date.now() + settleTimeoutMs;
    let confirmedBalance: bigint = 0n;
    while (Date.now() < deadline) {
      const { balance } = await wallet.getBalance();
      if (balance >= expectedBalance) {
        confirmedBalance = balance;
        break;
      }
      await new Promise((r) => setTimeout(r, 2_000));
    }

    if (output === "raw") {
      return rawResult({
        txid,
        leaves,
        claimedSats,
        confirmedBalance:
          confirmedBalance > 0n ? Number(confirmedBalance) : null,
        settled: confirmedBalance > 0n,
      });
    }

    if (confirmedBalance === 0n) {
      return {
        content: [
          {
            type: "text",
            text:
              `Deposit claimed (${formatSats(BigInt(claimedSats))}, txid: ${txid}) but balance has not settled yet. ` +
              `Call spark_get_balance in a few seconds to check.`,
          },
        ],
      };
    }

    return {
      content: [
        {
          type: "text",
          text:
            `Deposit claimed successfully.\n` +
            `Leaves created: ${leaves.length}\n` +
            `Claimed amount: ${formatSats(BigInt(claimedSats))}\n` +
            `Wallet balance: ${formatSats(confirmedBalance)}`,
        },
      ],
    };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}
