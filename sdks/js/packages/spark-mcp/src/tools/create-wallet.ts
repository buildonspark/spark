import { createFreshWallet } from "../wallet.js";
import {
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type CreateFreshFn = () => Promise<{ wallet: SparkWallet; mnemonic: string }>;

export async function handleCreateWallet(
  createFresh: CreateFreshFn = createFreshWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const { wallet, mnemonic } = await createFresh();
    const sparkAddress = await wallet.getSparkAddress();
    if (output === "raw") return rawResult({ mnemonic, sparkAddress });
    return {
      content: [
        {
          type: "text",
          text: [
            "New Spark wallet created.",
            `Mnemonic: ${mnemonic}`,
            `Spark address: ${sparkAddress}`,
            "",
            "Save the mnemonic — pass it to any tool as the `mnemonic` parameter to use this wallet again.",
          ].join("\n"),
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
