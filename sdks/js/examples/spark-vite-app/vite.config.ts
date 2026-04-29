import { Buffer } from "node:buffer";
import { execFileSync } from "node:child_process";
import type { IncomingMessage } from "node:http";
import fs from "node:fs";
import path from "node:path";
import { defineConfig, loadEnv, type Plugin, type ProxyOptions } from "vite";
import react from "@vitejs/plugin-react";
import {
  getLocalBitcoinRpcProxyPath,
  getLocalElectrsProxyPath,
  getLocalOperatorCount,
  getLocalOperatorProxyPath,
  getLocalSspProxyPath,
} from "./src/wallet-config.js";
import type { ConfigOptions } from "@buildonspark/spark-sdk";

function stripProxyPrefix(path: string, prefix: string) {
  if (!path.startsWith(prefix)) {
    return path;
  }

  const rewrittenPath = path.slice(prefix.length);
  return rewrittenPath.length > 0 ? rewrittenPath : "/";
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const base = env["SPARK_VITE_APP_BASE"] ?? "/";
  const configOverride = getConfigOverride(env["CONFIG_FILE"]);
  const privateConfigs = getPrivateConfigs();
  const localIngressHost = resolveLocalIngressHost(env);
  const hasLocalConfig = Boolean(localIngressHost);
  const operatorProxyEntries = Array.from(
    { length: getLocalOperatorCount(env, configOverride) },
    (_, index) => {
      const proxyPath = getLocalOperatorProxyPath(index);
      const minikubeHost = `${index}.spark-web.minikube.local`;
      return [
        proxyPath,
        {
          target:
            env[`VITE_LOCAL_SPARK_OPERATOR_${index}_TARGET`] ??
            getLocalOperatorTarget(index, localIngressHost),
          changeOrigin: true,
          secure: false,
          headers: getIngressHostHeaders(localIngressHost, minikubeHost),
          configure: createIngressHostForwarder(localIngressHost, minikubeHost),
          rewrite: (path: string) => stripProxyPrefix(path, proxyPath),
        },
      ] as const;
    },
  );

  const electrsProxyPath = getLocalElectrsProxyPath();
  const sspProxyPath = getLocalSspProxyPath();
  const bitcoinRpcProxyPath = getLocalBitcoinRpcProxyPath();

  return {
    base,
    envPrefix: ["VITE_SPARK_", "VITE_NUM_SPARK_OPERATORS"],
    plugins: [react(), createLocalOnlyProxyGuard(bitcoinRpcProxyPath)],
    define: {
      __SPARK_CONFIG_OVERRIDE__: JSON.stringify(configOverride),
      __SPARK_PRIVATE_CONFIGS__: JSON.stringify(privateConfigs),
      __SPARK_LOCAL_CONFIG_AVAILABLE__: JSON.stringify(hasLocalConfig),
    },
    server: {
      port: 5173,
      host: "0.0.0.0",
      proxy: Object.fromEntries([
        ...operatorProxyEntries,
        [
          electrsProxyPath,
          {
            target:
              env["VITE_LOCAL_ELECTRS_TARGET"] ??
              getLocalElectrsTarget(localIngressHost),
            changeOrigin: true,
            secure: false,
            headers: getIngressHostHeaders(
              localIngressHost,
              "mempool.minikube.local",
            ),
            configure: createIngressHostForwarder(
              localIngressHost,
              "mempool.minikube.local",
            ),
            rewrite: (path: string) => stripProxyPrefix(path, electrsProxyPath),
          },
        ],
        [
          sspProxyPath,
          {
            target:
              env["VITE_LOCAL_SSP_TARGET"] ??
              getLocalSspTarget(localIngressHost),
            changeOrigin: true,
            secure: false,
            headers: getIngressHostHeaders(
              localIngressHost,
              "app.minikube.local",
            ),
            configure: createIngressHostForwarder(
              localIngressHost,
              "app.minikube.local",
            ),
            rewrite: (path: string) => stripProxyPrefix(path, sspProxyPath),
          },
        ],
        [
          bitcoinRpcProxyPath,
          {
            target:
              env["VITE_LOCAL_BITCOIN_RPC_TARGET"] ??
              env["BITCOIN_RPC_URL"] ??
              getLocalBitcoinRpcTarget(localIngressHost),
            changeOrigin: true,
            headers: getBitcoinRpcHeaders(env),
            rewrite: (path: string) =>
              stripProxyPrefix(path, bitcoinRpcProxyPath),
          },
        ],
      ]),
    },
  };
});

function getPrivateConfigs(): {
  dev: Partial<Record<"MAINNET" | "REGTEST" | "TESTNET", ConfigOptions>>;
} {
  return {
    dev: {
      MAINNET: readPrivateConfig("dev-mainnet-config.json"),
      REGTEST: readPrivateConfig("dev-regtest-config.json"),
    },
  };
}

function getConfigOverride(
  configFile: string | undefined,
): ConfigOptions | undefined {
  if (!configFile) {
    return undefined;
  }

  return JSON.parse(
    fs.readFileSync(path.resolve(process.cwd(), configFile), "utf8"),
  ) as ConfigOptions;
}

function readPrivateConfig(filename: string): ConfigOptions | undefined {
  const configPath = path.resolve(
    process.cwd(),
    "../../private/config",
    filename,
  );

  if (!fs.existsSync(configPath)) {
    return undefined;
  }

  try {
    return JSON.parse(fs.readFileSync(configPath, "utf8")) as ConfigOptions;
  } catch (error) {
    throw new Error(
      `Failed to parse private config ${filename}: ${String(error)}`,
    );
  }
}

function getLocalOperatorTarget(
  index: number,
  localIngressHost: string,
): string {
  if (localIngressHost) {
    return `https://${localIngressHost}`;
  }

  return `https://localhost:${8535 + index}`;
}

function getLocalElectrsTarget(localIngressHost: string): string {
  return localIngressHost
    ? `http://${localIngressHost}/api`
    : "http://127.0.0.1:30000";
}

function getLocalSspTarget(localIngressHost: string): string {
  return localIngressHost
    ? `http://${localIngressHost}`
    : "http://127.0.0.1:5000";
}

function getLocalBitcoinRpcTarget(localIngressHost: string): string {
  return localIngressHost
    ? `http://${localIngressHost}:8332`
    : "http://127.0.0.1:8332";
}

function getBitcoinRpcHeaders(
  env: Record<string, string | undefined>,
): Record<string, string> {
  const user = env["BITCOIN_RPC_USER"] ?? "testutil";
  const pass = env["BITCOIN_RPC_PASSWORD"] ?? "testutilpassword";

  return {
    Authorization: `Basic ${Buffer.from(`${user}:${pass}`).toString("base64")}`,
  };
}

function isLoopbackAddress(address: string | undefined): boolean {
  const normalizedAddress = address?.replace(/^\[(.*)\]$/, "$1");

  return (
    normalizedAddress === "localhost" ||
    normalizedAddress === "127.0.0.1" ||
    normalizedAddress === "::1" ||
    normalizedAddress === "::ffff:127.0.0.1"
  );
}

function isLoopbackHost(hostHeader: string | undefined): boolean {
  if (!hostHeader) {
    return false;
  }

  try {
    return isLoopbackAddress(new URL(`http://${hostHeader}`).hostname);
  } catch {
    return false;
  }
}

function hasSameOriginLoopbackHeaders(req: IncomingMessage): boolean {
  const host = req.headers.host;
  if (!isLoopbackHost(host)) {
    return false;
  }

  const allowedOrigins = new Set([`http://${host}`, `https://${host}`]);
  const origin = req.headers.origin;
  if (origin) {
    return allowedOrigins.has(origin);
  }

  const referer = req.headers.referer;
  if (!referer) {
    return false;
  }

  try {
    return allowedOrigins.has(new URL(referer).origin);
  } catch {
    return false;
  }
}

function createLocalOnlyProxyGuard(proxyPath: string): Plugin {
  return {
    name: "local-only-bitcoin-rpc-proxy",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        if (!req.url?.startsWith(proxyPath)) {
          return next();
        }

        if (
          isLoopbackAddress(req.socket.remoteAddress) &&
          hasSameOriginLoopbackHeaders(req)
        ) {
          return next();
        }

        res.statusCode = 403;
        res.setHeader("Content-Type", "text/plain");
        res.end(
          "The local Bitcoin RPC proxy is only available from same-origin localhost requests.",
        );
      });
    },
  };
}

function getIngressHostHeaders(
  localIngressHost: string,
  host: string,
): Record<string, string> | undefined {
  if (!localIngressHost) {
    return undefined;
  }

  return {
    host,
  };
}

function createIngressHostForwarder(
  localIngressHost: string,
  host: string,
): ProxyOptions["configure"] | undefined {
  if (!localIngressHost) {
    return undefined;
  }

  return (proxy) => {
    proxy.on("proxyReq", (proxyReq) => {
      proxyReq.setHeader("host", host);
    });
  };
}

function resolveLocalIngressHost(
  env: Record<string, string | undefined>,
): string {
  const explicitHost = env["SPARK_LOCAL_INGRESS_HOST"]?.trim();

  if (explicitHost) {
    return explicitHost;
  }

  if (isKindLikeKubectlContext()) {
    return "127.0.0.1";
  }

  return getMinikubeIp();
}

function isKindLikeKubectlContext(): boolean {
  const currentContext = runCommand("kubectl", ["config", "current-context"]);
  if (!currentContext) {
    return false;
  }

  const normalizedContext = currentContext.toLowerCase();
  return (
    normalizedContext.includes("kind") || normalizedContext.includes("kdev")
  );
}

function getMinikubeIp(): string {
  return runCommand("minikube", ["ip"]);
}

function runCommand(command: string, args: string[]): string {
  try {
    return execFileSync(command, args, {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    }).trim();
  } catch {
    return "";
  }
}
