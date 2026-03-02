import { resolveWallet } from "../wallet.js";
import {
  formatSats,
  formatTransferList,
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

type WalletTransfer = {
  id: string;
  status: string;
  totalValue: number;
  createdTime: Date | undefined;
  updatedTime: Date | undefined;
  expiryTime: Date | undefined;
  type: string;
  transferDirection: string;
  senderIdentityPublicKey: string;
  receiverIdentityPublicKey: string;
  sparkInvoice: string | undefined;
  leaves: unknown[];
};

function formatTransfer(t: WalletTransfer): string {
  const createdAt = t.createdTime ? t.createdTime.toISOString() : "?";
  return `${t.id} | ${t.status} | ${t.totalValue} sats | ${createdAt}`;
}

function formatTransferVerbose(t: WalletTransfer): string {
  const lines = [
    `Transfer ID: ${t.id}`,
    `Status: ${t.status}`,
    `Type: ${t.type}`,
    `Direction: ${t.transferDirection}`,
    `Amount: ${formatSats(BigInt(t.totalValue))}`,
    `Sender: ${t.senderIdentityPublicKey}`,
    `Receiver: ${t.receiverIdentityPublicKey}`,
    `Created: ${t.createdTime?.toISOString() ?? "?"}`,
    `Updated: ${t.updatedTime?.toISOString() ?? "?"}`,
  ];
  if (t.expiryTime) lines.push(`Expires: ${t.expiryTime.toISOString()}`);
  if (t.sparkInvoice) lines.push(`Spark invoice: ${t.sparkInvoice}`);
  lines.push(`Leaves: ${t.leaves.length}`);
  return lines.join("\n");
}

export async function handleSendTransfer(
  receiverSparkAddress: string,
  amountSats: number,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);

    const { balance } = await wallet.getBalance();
    if (balance < BigInt(amountSats)) {
      return {
        content: [
          {
            type: "text",
            text:
              `Insufficient balance: wallet has ${formatSats(balance)} but transfer requires ${formatSats(BigInt(amountSats))}. ` +
              `If you recently claimed a deposit, the balance may still be settling — wait a few seconds and retry.`,
          },
        ],
        isError: true,
      };
    }

    const transfer = await wallet.transfer({
      receiverSparkAddress,
      amountSats,
    });

    if (output === "raw") return rawResult(transfer);
    if (output === "verbose") {
      return {
        content: [
          {
            type: "text",
            text: formatTransferVerbose(transfer as WalletTransfer),
          },
        ],
      };
    }

    return {
      content: [
        {
          type: "text",
          text: `Transfer sent.\nTransfer ID: ${transfer.id}\nStatus: ${transfer.status}\nAmount: ${amountSats} sats`,
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

export async function handleGetTransfer(
  id: string,
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const transfer = await wallet.getTransfer(id);
    if (!transfer) {
      return {
        content: [{ type: "text", text: `Transfer not found: ${id}` }],
        isError: true,
      };
    }

    if (output === "raw") return rawResult(transfer);
    if (output === "verbose") {
      return {
        content: [
          {
            type: "text",
            text: formatTransferVerbose(transfer as WalletTransfer),
          },
        ],
      };
    }

    return {
      content: [
        { type: "text", text: formatTransfer(transfer as WalletTransfer) },
      ],
    };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}

export async function handleListTransfers(
  mnemonic?: string,
  resolve: ResolveFn = resolveWallet,
  output: OutputMode = "normal",
): Promise<ToolResult> {
  try {
    const wallet = await resolve(mnemonic);
    const { transfers } = await wallet.getTransfers(10, 0);

    if (output === "raw") return rawResult(transfers);
    if (output === "verbose") {
      if (transfers.length === 0) {
        return { content: [{ type: "text", text: "No transfers found." }] };
      }
      const blocks = (transfers as WalletTransfer[]).map(formatTransferVerbose);
      return {
        content: [
          {
            type: "text",
            text: `${transfers.length} transfer${transfers.length === 1 ? "" : "s"} (most recent first):\n\n${blocks.join("\n\n---\n\n")}`,
          },
        ],
      };
    }

    const lines = (transfers as WalletTransfer[]).map(formatTransfer);
    return { content: [{ type: "text", text: formatTransferList(lines) }] };
  } catch (err) {
    return {
      content: [{ type: "text", text: `Error: ${errorMessage(err)}` }],
      isError: true,
    };
  }
}
