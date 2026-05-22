import { z } from "zod";

export type OutputMode = "normal" | "verbose" | "raw";

export const outputParam = z
  .enum(["normal", "verbose", "raw"])
  .optional()
  .describe(
    "Output detail level: normal (default, concise), verbose (all fields, human-readable), raw (full JSON from SDK)",
  );

export type ToolResult = {
  content: Array<{ type: "text"; text: string }>;
  isError?: boolean;
};

function bigintReplacer(_key: string, value: unknown): unknown {
  return typeof value === "bigint" ? Number(value) : value;
}

export function rawResult(data: unknown): ToolResult {
  return {
    content: [{ type: "text", text: JSON.stringify(data, bigintReplacer, 2) }],
  };
}

export function formatSats(sats: bigint): string {
  return `${sats.toLocaleString("en-US")} sats`;
}

export function errorMessage(err: unknown): string {
  if (err == null) return "Unknown error";
  if (err instanceof Error) return err.message;
  if (typeof err === "object") {
    try {
      return JSON.stringify(err);
    } catch {
      return "Unknown error";
    }
  }
  if (
    typeof err === "string" ||
    typeof err === "number" ||
    typeof err === "boolean" ||
    typeof err === "bigint"
  ) {
    return err.toString();
  }
  if (typeof err === "symbol") {
    return err.description ?? "Unknown error";
  }
  return "Unknown error";
}

export function formatTransferList(items: string[], limit = 10): string {
  if (items.length === 0) return "No transfers found.";
  const shown = items.slice(0, limit);
  const header =
    items.length > limit
      ? `${items.length} transfers (showing ${limit} of ${items.length}), most recent first:`
      : `${items.length} transfer${items.length === 1 ? "" : "s"} (most recent first):`;
  return [header, ...shown.map((item, i) => `${i + 1}. ${item}`)].join("\n");
}
