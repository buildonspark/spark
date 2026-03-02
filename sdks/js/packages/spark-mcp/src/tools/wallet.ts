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

export async function handleGetBalance(
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const balance = await wallet.getBalance();
    if (output === "raw") return rawResult(balance);
    return {
      content: [
        { type: "text", text: `Balance: ${formatSats(balance.balance)}` },
      ],
    };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

export async function handleGetSparkAddress(
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const addr = await wallet.getSparkAddress();
    if (output === "raw") return rawResult({ sparkAddress: addr });
    return { content: [{ type: "text", text: `Spark address: ${addr}` }] };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}
