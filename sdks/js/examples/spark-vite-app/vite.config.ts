import fs from "node:fs";
import path from "node:path";
import { defineConfig, loadEnv, type ProxyOptions } from "vite";
import react from "@vitejs/plugin-react";
import {
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
  const configOverride = getConfigOverride(env["CONFIG_FILE"]);
  const privateConfigs = getPrivateConfigs();
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
            getLocalOperatorTarget(index, env),
          changeOrigin: true,
          secure: false,
          headers: getMinikubeHeaders(env["MINIKUBE_IP"], minikubeHost),
          configure: createMinikubeHostForwarder(
            env["MINIKUBE_IP"],
            minikubeHost,
          ),
          rewrite: (path: string) => stripProxyPrefix(path, proxyPath),
        },
      ] as const;
    },
  );

  const electrsProxyPath = getLocalElectrsProxyPath();
  const sspProxyPath = getLocalSspProxyPath();

  return {
    plugins: [react()],
    define: {
      __SPARK_CONFIG_OVERRIDE__: JSON.stringify(configOverride),
      __SPARK_PRIVATE_CONFIGS__: JSON.stringify(privateConfigs),
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
              env["VITE_LOCAL_ELECTRS_TARGET"] ?? getLocalElectrsTarget(env),
            changeOrigin: true,
            secure: false,
            headers: getMinikubeHeaders(
              env["MINIKUBE_IP"],
              "mempool.minikube.local",
            ),
            configure: createMinikubeHostForwarder(
              env["MINIKUBE_IP"],
              "mempool.minikube.local",
            ),
            rewrite: (path: string) => stripProxyPrefix(path, electrsProxyPath),
          },
        ],
        [
          sspProxyPath,
          {
            target: env["VITE_LOCAL_SSP_TARGET"] ?? getLocalSspTarget(env),
            changeOrigin: true,
            secure: false,
            headers: getMinikubeHeaders(
              env["MINIKUBE_IP"],
              "app.minikube.local",
            ),
            configure: createMinikubeHostForwarder(
              env["MINIKUBE_IP"],
              "app.minikube.local",
            ),
            rewrite: (path: string) => stripProxyPrefix(path, sspProxyPath),
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
  env: Record<string, string | undefined>,
): string {
  if (env["MINIKUBE_IP"]) {
    return `https://${env["MINIKUBE_IP"]}`;
  }

  return `https://localhost:${8535 + index}`;
}

function getLocalElectrsTarget(
  env: Record<string, string | undefined>,
): string {
  return env["MINIKUBE_IP"]
    ? `http://${env["MINIKUBE_IP"]}/api`
    : "http://127.0.0.1:30000";
}

function getLocalSspTarget(env: Record<string, string | undefined>): string {
  return env["MINIKUBE_IP"]
    ? `http://${env["MINIKUBE_IP"]}`
    : "http://127.0.0.1:5000";
}

function getMinikubeHeaders(
  minikubeIp: string | undefined,
  host: string,
): Record<string, string> | undefined {
  if (!minikubeIp) {
    return undefined;
  }

  return {
    host,
  };
}

function createMinikubeHostForwarder(
  minikubeIp: string | undefined,
  host: string,
): ProxyOptions["configure"] | undefined {
  if (!minikubeIp) {
    return undefined;
  }

  return (proxy) => {
    proxy.on("proxyReq", (proxyReq) => {
      proxyReq.setHeader("host", host);
    });
  };
}
