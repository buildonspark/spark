import {
  formatSats,
  errorMessage,
  rawResult,
  type OutputMode,
  type ToolResult,
} from "../utils.js";
import { getServerConfig } from "../config.js";

type RpcResponse = {
  result: unknown;
  error: { message: string } | null;
};

async function bitcoinRpc(
  method: string,
  params: unknown[],
  fetchFn: typeof fetch,
): Promise<unknown> {
  const minikubeIp = process.env["MINIKUBE_IP"];
  const defaultUrl = minikubeIp
    ? `http://${minikubeIp}:8332`
    : "http://127.0.0.1:8332";
  const url = process.env["BITCOIN_RPC_URL"] ?? defaultUrl;
  const user = process.env["BITCOIN_RPC_USER"] ?? "testutil";
  const pass = process.env["BITCOIN_RPC_PASSWORD"] ?? "testutilpassword";

  const response = await fetchFn(url, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Authorization: `Basic ${Buffer.from(`${user}:${pass}`).toString("base64")}`,
    },
    body: JSON.stringify({ jsonrpc: "1.0", id: "spark-mcp", method, params }),
  });

  if (!response.ok) {
    throw new Error(`Bitcoin RPC HTTP error: ${response.status}`);
  }

  const data = (await response.json()) as RpcResponse;
  if (data.error) {
    throw new Error(`Bitcoin RPC error: ${data.error.message}`);
  }
  return data.result;
}

export async function handleFundAddress(
  address: string,
  amountSats: number = 50_000,
  blocksToMine: number = 1,
  fetchFn: typeof fetch = fetch,
  chainWatcherDelayMs: number = 3000,
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
            `spark_fund_address only works on the LOCAL network (self-hosted regtest). ` +
            `Current network: ${network}.\n` +
            `Fund the deposit address through a faucet or external wallet instead.`,
        },
      ],
      isError: true,
    };
  }

  try {
    const amountBtc = amountSats / 100_000_000;

    const txid = (await bitcoinRpc(
      "sendtoaddress",
      [address, amountBtc],
      fetchFn,
    )) as string;

    const miningAddress = (await bitcoinRpc(
      "getnewaddress",
      [],
      fetchFn,
    )) as string;

    await bitcoinRpc(
      "generatetoaddress",
      [blocksToMine, miningAddress],
      fetchFn,
    );

    // Wait for the Spark operator's chain watcher to process the new block.
    // The chain watcher must mark the deposit address as confirmed before
    // claimDeposit() can create the leaf in AVAILABLE status.
    if (chainWatcherDelayMs > 0) {
      await new Promise((r) => setTimeout(r, chainWatcherDelayMs));
    }

    if (output === "raw") {
      return rawResult({ txid, address, amountSats, blocksToMine });
    }

    return {
      content: [
        {
          type: "text",
          text:
            `Funded ${formatSats(BigInt(amountSats))} to ${address}\n` +
            `Transaction ID: ${txid}\n` +
            `Mined ${blocksToMine} blocks to confirm.\n` +
            `IMPORTANT: This deposit address is now spent. Call spark_get_deposit_address for a fresh address before the next deposit.`,
        },
      ],
    };
  } catch (err) {
    return {
      content: [
        { type: "text", text: `Failed to fund address: ${errorMessage(err)}` },
      ],
      isError: true,
    };
  }
}
