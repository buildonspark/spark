import { resolveWallet, evictWallet } from "../wallet.js";
import type { BitcoinNetwork } from "../config.js";
import {
  formatSats,
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import type { SparkWallet } from "@buildonspark/spark-sdk";

type ResolveFn = (mnemonic?: string) => Promise<SparkWallet>;

export async function handleGetBalance({
  mnemonic,
  resolve = resolveWallet,
  output = "normal",
}: {
  mnemonic?: string;
  resolve?: ResolveFn;
  output?: OutputMode;
}): Promise<ToolResult> {
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

export async function handleGetSparkAddress({
  mnemonic,
  resolve = resolveWallet,
  output = "normal",
}: {
  mnemonic?: string;
  resolve?: ResolveFn;
  output?: OutputMode;
}): Promise<ToolResult> {
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

type EvictFn = (
  mnemonic?: string,
  networkOverride?: BitcoinNetwork,
) => Promise<boolean>;

export async function handleDisconnectWallet({
  mnemonic,
  networkOverride,
  evict = evictWallet,
  output = "normal",
}: {
  mnemonic?: string;
  networkOverride?: BitcoinNetwork;
  evict?: EvictFn;
  output?: OutputMode;
}): Promise<ToolResult> {
  try {
    const evicted = await evict(mnemonic, networkOverride);

    if (output === "raw") return rawResult({ evicted });

    if (!evicted) {
      return {
        content: [
          {
            type: "text",
            text: "No cached wallet found for this mnemonic/network. Nothing to disconnect.",
          },
        ],
      };
    }

    return {
      content: [
        {
          type: "text",
          text: "Wallet disconnected. Background stream stopped and connections closed. The next call will create a fresh instance.",
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
