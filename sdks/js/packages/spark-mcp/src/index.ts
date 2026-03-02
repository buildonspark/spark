import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { registerAllTools } from "./tools/index.js";
import { getServerConfig } from "./config.js";

async function main(): Promise<void> {
  const config = getServerConfig();
  const hasDefaultWallet = !!process.env["SPARK_MNEMONIC"];

  const instructions = [
    `Spark wallet MCP server on ${config.defaultNetwork}.`,
    "",
    "Network:",
    `- Default network: ${config.defaultNetwork}`,
    "- Pass a `network` parameter (LOCAL, REGTEST, or MAINNET) to any tool to override the default for that call.",
    "- LOCAL = self-hosted regtest infrastructure (minikube or run-everything.sh). Funding tools available.",
    "- REGTEST = Lightspark-hosted regtest infrastructure. Real signing operators, test Bitcoin.",
    "- MAINNET = production Bitcoin. Real signing operators, real Bitcoin.",
    "",
    "Wallet identity:",
    "- Call spark_create_wallet to generate a brand new wallet. It returns a mnemonic — save it to use that wallet again.",
    "- Pass mnemonic to any tool to operate on a specific wallet, regardless of server defaults.",
    `- Omit mnemonic to use the server default (SPARK_MNEMONIC env var).${
      hasDefaultWallet
        ? " A default wallet is currently configured."
        : " No default wallet is configured — you must pass a mnemonic or call spark_create_wallet first."
    }`,
    "",
    "Deposit workflow tips:",
    "- Deposit addresses are SINGLE-USE. After funding and claiming a deposit, call spark_get_deposit_address again for the next deposit.",
    "- spark_claim_deposit waits for the balance to settle before returning, so funds are immediately spendable.",
    ...(config.defaultNetwork === "LOCAL"
      ? [
          "- spark_deposit and spark_fund_address are available for automated funding via local Bitcoin RPC.",
        ]
      : [
          "- Fund deposit addresses through an external Bitcoin wallet or faucet.",
        ]),
    "",
    "Output modes:",
    '- All tools accept an optional `output` parameter: "normal" (default, concise), "verbose" (all fields, human-readable), or "raw" (full JSON from SDK).',
  ].join("\n");

  const server = new McpServer(
    {
      name: "spark-mcp",
      version: "0.2.0",
    },
    { instructions },
  );

  registerAllTools(server, config);

  const transport = new StdioServerTransport();
  await server.connect(transport);
}

main().catch((err) => {
  process.stderr.write(
    `Unexpected error: ${err instanceof Error ? err.message : String(err)}\n`,
  );
  process.exit(1);
});
