export type BitcoinNetwork = "MAINNET" | "REGTEST" | "LOCAL";

export type ServerConfig = {
  defaultNetwork: BitcoinNetwork;
};

const VALID_NETWORKS = new Set<string>(["MAINNET", "REGTEST", "LOCAL"]);

/**
 * Resolve server configuration from environment variables.
 *
 * BITCOIN_NETWORK (LOCAL | REGTEST | MAINNET) — defaults to REGTEST.
 */
export function getServerConfig(): ServerConfig {
  const raw = process.env["BITCOIN_NETWORK"] ?? "REGTEST";

  if (!VALID_NETWORKS.has(raw)) {
    throw new Error(
      `Invalid BITCOIN_NETWORK: "${raw}". Must be LOCAL, REGTEST, or MAINNET.\n\n` +
        "Networks:\n" +
        "  LOCAL    — self-hosted regtest (minikube or run-everything.sh)\n" +
        "  REGTEST  — Lightspark-hosted regtest (default)\n" +
        "  MAINNET  — production Bitcoin",
    );
  }

  return { defaultNetwork: raw as BitcoinNetwork };
}
