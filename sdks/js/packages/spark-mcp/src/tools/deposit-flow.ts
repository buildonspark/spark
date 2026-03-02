import { resolveWallet } from "../wallet.js";
import {
  formatSats,
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import { getServerConfig } from "../config.js";
import { handleFundAddress } from "./funding.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

/**
 * Combined deposit flow for LOCAL environments: get a deposit address, fund it
 * via local bitcoind RPC, claim the deposit, and wait for the balance to settle.
 * Returns the confirmed wallet balance.
 */
export async function handleDeposit(
  amountSats: number = 50_000,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  fundFn: typeof handleFundAddress = handleFundAddress,
  output: OutputMode = "normal",
  networkOverride?: string,
): Promise<ToolResult> {
  const config = getServerConfig();
  const network = networkOverride ?? config.defaultNetwork;
  if (network !== "LOCAL") {
    return {
      content: [
        {
          type: "text",
          text:
            `spark_deposit only works on the LOCAL network (self-hosted regtest). ` +
            `Current network: ${network}.\n` +
            `Use spark_get_deposit_address + external funding + spark_claim_deposit instead.`,
        },
      ],
      isError: true,
    };
  }

  try {
    const wallet = await resolve(mnemonic);

    // Step 1: Get a fresh single-use deposit address
    const address = await wallet.getSingleUseDepositAddress();

    // Step 2: Fund the address via local bitcoind RPC
    const fundResult = await fundFn(address, amountSats);
    if (fundResult.isError) {
      return fundResult;
    }

    // Extract txid from fund result
    const fundText = fundResult.content[0]?.text ?? "";
    const txidMatch = fundText.match(/Transaction ID: (\S+)/);
    if (!txidMatch) {
      return {
        content: [
          {
            type: "text",
            text: `Funding succeeded but could not extract transaction ID from result: ${fundText}`,
          },
        ],
        isError: true,
      };
    }
    const txid = txidMatch[1]!;

    // Step 3: Claim the deposit
    const leaves = await wallet.claimDeposit(txid);
    const claimedSats = leaves.reduce(
      (sum: number, l: { value: number }) => sum + l.value,
      0,
    );

    // Step 4: Wait for balance to settle
    const deadline = Date.now() + 30_000;
    let confirmedBalance: bigint = 0n;
    while (Date.now() < deadline) {
      const { balance } = await wallet.getBalance();
      if (balance >= BigInt(claimedSats)) {
        confirmedBalance = balance;
        break;
      }
      await new Promise((r) => setTimeout(r, 2_000));
    }

    if (output === "raw") {
      return rawResult({
        txid,
        depositAddress: address,
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
            `Deposit complete.\n` +
            `Deposited: ${formatSats(BigInt(claimedSats))}\n` +
            `Transaction ID: ${txid}\n` +
            `Wallet balance: ${formatSats(confirmedBalance)}`,
        },
      ],
    };
  } catch (err) {
    return {
      content: [
        {
          type: "text",
          text: `Error: ${errorMessage(err)}`,
        },
      ],
      isError: true,
    };
  }
}
