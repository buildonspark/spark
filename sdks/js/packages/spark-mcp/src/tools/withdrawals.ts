import { resolveWallet } from "../wallet.js";
import {
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";
import { ExitSpeed } from "@buildonspark/spark-sdk/types";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

type CurrencyAmount = {
  originalValue: number;
  originalUnit: string;
};

type CoopExitFeeQuote = {
  id: string;
  expiresAt: string;
  userFeeFast?: CurrencyAmount;
  userFeeMedium?: CurrencyAmount;
  userFeeSlow?: CurrencyAmount;
  l1BroadcastFeeFast?: CurrencyAmount;
  l1BroadcastFeeMedium?: CurrencyAmount;
  l1BroadcastFeeSlow?: CurrencyAmount;
};

export async function handleGetWithdrawalFeeQuote(
  amountSats: number,
  withdrawalAddress: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const quote = await wallet.getWithdrawalFeeQuote({
      amountSats,
      withdrawalAddress,
    });
    if (!quote) {
      return {
        content: [{ type: "text", text: `No fee quote available` }],
        isError: true,
      };
    }

    if (output === "raw") return rawResult(quote);

    const typedQuote = quote as CoopExitFeeQuote;
    const fastFee =
      (typedQuote.userFeeFast?.originalValue ?? 0) +
      (typedQuote.l1BroadcastFeeFast?.originalValue ?? 0);
    const mediumFee =
      (typedQuote.userFeeMedium?.originalValue ?? 0) +
      (typedQuote.l1BroadcastFeeMedium?.originalValue ?? 0);
    const slowFee =
      (typedQuote.userFeeSlow?.originalValue ?? 0) +
      (typedQuote.l1BroadcastFeeSlow?.originalValue ?? 0);

    const lines = [
      `Withdrawal fee quote:`,
      `Fast fee: ${fastFee} sats`,
      `Medium fee: ${mediumFee} sats`,
      `Slow fee: ${slowFee} sats`,
      `Quote ID: ${typedQuote.id}`,
      `Expires: ${typedQuote.expiresAt}`,
    ];
    if (output === "verbose") {
      lines.push(
        "",
        "Fee breakdown:",
        `  Fast:   user ${typedQuote.userFeeFast?.originalValue ?? 0} + L1 ${typedQuote.l1BroadcastFeeFast?.originalValue ?? 0} sats`,
        `  Medium: user ${typedQuote.userFeeMedium?.originalValue ?? 0} + L1 ${typedQuote.l1BroadcastFeeMedium?.originalValue ?? 0} sats`,
        `  Slow:   user ${typedQuote.userFeeSlow?.originalValue ?? 0} + L1 ${typedQuote.l1BroadcastFeeSlow?.originalValue ?? 0} sats`,
      );
    }
    return { content: [{ type: "text", text: lines.join("\n") }] };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

export async function handleWithdraw(
  onchainAddress: string,
  exitSpeed: "FAST" | "MEDIUM" | "SLOW",
  amountSats?: number,
  feeQuoteId?: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);

    // Always fetch a fee quote to get both the quote ID and fee amount,
    // since the SDK requires feeAmountSats (not just feeQuoteId).
    const quote = await wallet.getWithdrawalFeeQuote({
      amountSats: amountSats ?? 0,
      withdrawalAddress: onchainAddress,
    });
    if (!quote) {
      return {
        content: [{ type: "text", text: "No fee quote available" }],
        isError: true,
      };
    }
    const typedQuote = quote as CoopExitFeeQuote;
    let feeAmountSats: number;
    switch (exitSpeed) {
      case "FAST":
        feeAmountSats =
          (typedQuote.userFeeFast?.originalValue ?? 0) +
          (typedQuote.l1BroadcastFeeFast?.originalValue ?? 0);
        break;
      case "MEDIUM":
        feeAmountSats =
          (typedQuote.userFeeMedium?.originalValue ?? 0) +
          (typedQuote.l1BroadcastFeeMedium?.originalValue ?? 0);
        break;
      case "SLOW":
        feeAmountSats =
          (typedQuote.userFeeSlow?.originalValue ?? 0) +
          (typedQuote.l1BroadcastFeeSlow?.originalValue ?? 0);
        break;
    }

    const result = await wallet.withdraw({
      onchainAddress,
      exitSpeed: exitSpeed as ExitSpeed,
      amountSats,
      feeQuoteId: typedQuote.id,
      feeAmountSats,
    });

    if (output === "raw") return rawResult(result);

    return {
      content: [
        {
          type: "text",
          text: `Withdrawal initiated.\nRequest ID: ${result?.id ?? "?"}\nStatus: ${result?.status ?? "?"}`,
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
