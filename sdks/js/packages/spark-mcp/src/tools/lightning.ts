import { resolveWallet } from "../wallet.js";
import {
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

export async function handleCreateInvoice(
  amountSats: number,
  memo?: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const request = await wallet.createLightningInvoice({ amountSats, memo });

    if (output === "raw") return rawResult(request);

    const bolt11 = request.invoice.encodedInvoice;
    const lines = [
      `Invoice created.`,
      `BOLT11: ${bolt11}`,
      `Amount: ${amountSats} sats`,
    ];
    if (memo) lines.push(`Memo: ${memo}`);
    return { content: [{ type: "text", text: lines.join("\n") }] };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

export async function handlePayInvoice(
  invoice: string,
  maxFeeSats: number,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const result = await wallet.payLightningInvoice({ invoice, maxFeeSats });

    if (output === "raw") return rawResult(result);

    return {
      content: [
        {
          type: "text",
          text: `Payment successful.\nPayment ID: ${result.id ?? "?"}`,
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

export async function handleGetLightningFeeEstimate(
  invoice: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const feeSats = await wallet.getLightningSendFeeEstimate({
      encodedInvoice: invoice,
    });

    if (output === "raw") return rawResult({ feeSats });

    return {
      content: [
        {
          type: "text",
          text: `Estimated fee: ${feeSats} sats`,
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
