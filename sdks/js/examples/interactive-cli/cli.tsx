import {
  SparkWallet,
  SparkWalletEvent,
  WalletConfig,
  type ConfigOptions,
} from "@buildonspark/spark-sdk";
import * as fs from "fs";
import { Box, Text, render, useApp, useInput } from "ink";
import * as path from "path";
import React, { useCallback, useState } from "react";

type Network = "MAINNET" | "REGTEST" | "LOCAL";

type ActionKey =
  | "init"
  | "setMnemonic"
  | "getBalance"
  | "getSparkAddress"
  | "getIdentityKey"
  | "getDepositAddress"
  | "createInvoice"
  | "quit";

type Action = {
  label: string;
  value: ActionKey;
  requiresWallet?: boolean;
};

type PromptKind = "mnemonic" | "invoiceAmount";

type PromptState = {
  kind: PromptKind;
  value: string;
};

type ConfigState = {
  network: Network;
  config: ConfigOptions;
  source: string;
  warning?: string;
};

const ACTIONS: Action[] = [
  { label: "Initialize wallet", value: "init" },
  { label: "Set mnemonic or seed", value: "setMnemonic" },
  { label: "Get balance", value: "getBalance", requiresWallet: true },
  {
    label: "Get Spark address",
    value: "getSparkAddress",
    requiresWallet: true,
  },
  {
    label: "Get identity public key",
    value: "getIdentityKey",
    requiresWallet: true,
  },
  {
    label: "Get deposit address",
    value: "getDepositAddress",
    requiresWallet: true,
  },
  {
    label: "Create lightning invoice",
    value: "createInvoice",
    requiresWallet: true,
  },
  { label: "Quit", value: "quit" },
];

const formatError = (error: unknown) =>
  error instanceof Error ? error.message : String(error);

const resolveNetwork = (): Network => {
  const envNetwork = process.env.NETWORK?.toUpperCase();
  if (envNetwork === "MAINNET") return "MAINNET";
  if (envNetwork === "LOCAL") return "LOCAL";
  return "REGTEST";
};

const defaultConfigForNetwork = (network: Network): ConfigOptions => {
  switch (network) {
    case "MAINNET":
      return WalletConfig.MAINNET;
    case "LOCAL":
      return WalletConfig.LOCAL;
    case "REGTEST":
    default:
      return WalletConfig.REGTEST;
  }
};

const loadConfigState = (): ConfigState => {
  const network = resolveNetwork();
  const configFile = process.env.CONFIG_FILE;
  const fallback = defaultConfigForNetwork(network);
  const fallbackSource = `WalletConfig.${network}`;

  if (!configFile) {
    return { network, config: fallback, source: fallbackSource };
  }

  const resolvedConfigPath = path.resolve(configFile);
  try {
    const raw = fs.readFileSync(resolvedConfigPath, "utf8");
    const parsed = JSON.parse(raw) as ConfigOptions;

    if (parsed.network && parsed.network !== network) {
      return {
        network,
        config: fallback,
        source: fallbackSource,
        warning: `CONFIG_FILE network (${parsed.network}) does not match NETWORK (${network}); using defaults.`,
      };
    }

    return {
      network,
      config: { ...fallback, ...parsed, network },
      source: resolvedConfigPath,
    };
  } catch (error) {
    return {
      network,
      config: fallback,
      source: fallbackSource,
      warning: `Failed to read CONFIG_FILE (${resolvedConfigPath}): ${formatError(
        error,
      )}`,
    };
  }
};

const App = () => {
  const { exit } = useApp();
  const [configState] = useState<ConfigState>(() => loadConfigState());
  const [wallet, setWallet] = useState<SparkWallet | null>(null);
  const [logs, setLogs] = useState<string[]>(() =>
    configState.warning ? [`Warning: ${configState.warning}`] : [],
  );
  const [isBusy, setIsBusy] = useState(false);
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [prompt, setPrompt] = useState<PromptState | null>(null);
  const [mnemonicOrSeed, setMnemonicOrSeed] = useState<string>(
    process.env.SPARK_MNEMONIC ?? "",
  );
  const [invoiceAmount, setInvoiceAmount] = useState<string>("100");

  const appendLog = useCallback((line: string) => {
    setLogs((prev) => {
      const next = [...prev, line];
      return next.slice(-200);
    });
  }, []);

  const cleanupAndExit = useCallback(async () => {
    if (wallet) {
      try {
        await wallet.cleanupConnections();
      } catch (error) {
        appendLog(`Cleanup error: ${formatError(error)}`);
      }
    }
    exit();
  }, [wallet, exit, appendLog]);

  const attachWalletListeners = useCallback(
    (newWallet: SparkWallet) => {
      newWallet.on(
        SparkWalletEvent.DepositConfirmed,
        (depositId: string, balance: bigint) => {
          appendLog(
            `Deposit confirmed: ${depositId} (balance ${balance} sats)`,
          );
        },
      );
      newWallet.on(
        SparkWalletEvent.TransferClaimed,
        (transferId: string, balance: bigint) => {
          appendLog(
            `Transfer claimed: ${transferId} (balance ${balance} sats)`,
          );
        },
      );
      newWallet.on(SparkWalletEvent.StreamConnected, () => {
        appendLog("Stream connected");
      });
      newWallet.on(
        SparkWalletEvent.StreamReconnecting,
        (
          attempt: number,
          maxAttempts: number,
          delayMs: number,
          error: string,
        ) => {
          appendLog(
            `Stream reconnecting (attempt ${attempt}/${maxAttempts}, delay ${delayMs}ms): ${error}`,
          );
        },
      );
      newWallet.on(SparkWalletEvent.StreamDisconnected, (reason: string) => {
        appendLog(`Stream disconnected: ${reason}`);
      });
    },
    [appendLog],
  );

  const runWalletAction = useCallback(
    async (action: ActionKey) => {
      if (action === "quit") {
        await cleanupAndExit();
        return;
      }

      if (action === "setMnemonic") {
        setPrompt({ kind: "mnemonic", value: mnemonicOrSeed });
        return;
      }

      if (action === "createInvoice") {
        if (!wallet) {
          appendLog("Initialize wallet first.");
          return;
        }
        setPrompt({ kind: "invoiceAmount", value: invoiceAmount });
        return;
      }

      if (action === "init") {
        setIsBusy(true);
        try {
          appendLog("Initializing wallet...");
          if (wallet) {
            await wallet.cleanupConnections();
          }

          const seedInput = mnemonicOrSeed.trim();
          const { wallet: newWallet, mnemonic } = await SparkWallet.initialize({
            mnemonicOrSeed: seedInput.length > 0 ? seedInput : undefined,
            options: {
              ...configState.config,
              network: configState.network,
            },
          });
          setWallet(newWallet);
          attachWalletListeners(newWallet);
          appendLog(`Wallet initialized on ${configState.network}.`);
          if (mnemonic) {
            appendLog(`Mnemonic: ${mnemonic}`);
          }
        } catch (error) {
          appendLog(`Initialization failed: ${formatError(error)}`);
        } finally {
          setIsBusy(false);
        }
        return;
      }

      if (!wallet) {
        appendLog("Initialize wallet first.");
        return;
      }

      setIsBusy(true);
      try {
        switch (action) {
          case "getBalance": {
            const balance = await wallet.getBalance();
            appendLog(`Balance: ${balance.balance} sats`);
            if (balance.tokenBalances.size > 0) {
              appendLog(
                `Token balances: ${balance.tokenBalances.size} token types`,
              );
            }
            break;
          }
          case "getSparkAddress": {
            const address = await wallet.getSparkAddress();
            appendLog(`Spark address: ${address}`);
            break;
          }
          case "getIdentityKey": {
            const key = await wallet.getIdentityPublicKey();
            appendLog(`Identity public key: ${key}`);
            break;
          }
          case "getDepositAddress": {
            const address = await wallet.getSingleUseDepositAddress();
            appendLog(`Deposit address: ${address}`);
            break;
          }
          default:
            break;
        }
      } catch (error) {
        appendLog(`Action failed: ${formatError(error)}`);
      } finally {
        setIsBusy(false);
      }
    },
    [
      attachWalletListeners,
      appendLog,
      cleanupAndExit,
      configState.config,
      configState.network,
      invoiceAmount,
      mnemonicOrSeed,
      wallet,
    ],
  );

  const submitPrompt = useCallback(
    async (state: PromptState) => {
      if (state.kind === "mnemonic") {
        const value = state.value.trim();
        setMnemonicOrSeed(value);
        appendLog(
          value ? "Mnemonic or seed updated." : "Mnemonic or seed cleared.",
        );
        return;
      }

      if (state.kind === "invoiceAmount") {
        if (!wallet) {
          appendLog("Initialize wallet first.");
          return;
        }

        const raw = state.value.trim();
        const resolved = raw.length > 0 ? raw : invoiceAmount;
        const amount = Number(resolved);

        if (!Number.isFinite(amount) || amount < 0) {
          appendLog("Invoice amount must be a non-negative number.");
          return;
        }
        if (!Number.isSafeInteger(amount)) {
          appendLog("Invoice amount must be less than 2^53.");
          return;
        }

        setInvoiceAmount(resolved);
        setIsBusy(true);
        try {
          const invoice = await wallet.createLightningInvoice({
            amountSats: amount,
          });
          appendLog(`Invoice created: ${invoice.invoice.encodedInvoice}`);
        } catch (error) {
          appendLog(`Invoice failed: ${formatError(error)}`);
        } finally {
          setIsBusy(false);
        }
      }
    },
    [appendLog, invoiceAmount, wallet],
  );

  useInput((input, key) => {
    if (key.ctrl && input === "c") {
      void cleanupAndExit();
      return;
    }

    if (prompt) {
      if (key.escape) {
        setPrompt(null);
        appendLog("Prompt cancelled.");
        return;
      }
      if (key.return) {
        const current = prompt;
        setPrompt(null);
        void submitPrompt(current);
        return;
      }
      if (key.backspace || key.delete) {
        setPrompt({ ...prompt, value: prompt.value.slice(0, -1) });
        return;
      }
      if (input && !key.ctrl && !key.meta) {
        setPrompt({ ...prompt, value: prompt.value + input });
      }
      return;
    }

    if (isBusy) {
      if (input === "q") {
        void cleanupAndExit();
      }
      return;
    }

    if (key.upArrow) {
      setSelectedIndex((prev) => (prev === 0 ? ACTIONS.length - 1 : prev - 1));
      return;
    }

    if (key.downArrow) {
      setSelectedIndex((prev) => (prev === ACTIONS.length - 1 ? 0 : prev + 1));
      return;
    }

    if (key.return) {
      const selected = ACTIONS[selectedIndex];
      if (!selected) {
        return;
      }
      if (selected.requiresWallet && !wallet) {
        appendLog("Initialize wallet first.");
        return;
      }
      void runWalletAction(selected.value);
      return;
    }

    if (input === "q") {
      void cleanupAndExit();
    }
  });

  const visibleLogs = logs.slice(-10);

  return (
    <Box flexDirection="column">
      <Text color="cyan">Spark Interactive CLI</Text>
      <Text>
        Network: {configState.network} | Config: {configState.source}
      </Text>
      <Text>
        Mnemonic or seed: {mnemonicOrSeed ? "set" : "empty"} | Wallet:{" "}
        {wallet ? "initialized" : "not initialized"}
      </Text>
      <Text>Invoice amount (sats): {invoiceAmount}</Text>

      <Box marginTop={1} flexDirection="column">
        {prompt ? (
          <Box flexDirection="column">
            <Text>
              {prompt.kind === "mnemonic"
                ? "Mnemonic or seed (leave blank to generate)"
                : "Invoice amount in sats"}
            </Text>
            <Text>{prompt.value || ""}</Text>
            <Text dimColor>Enter to submit, Esc to cancel</Text>
          </Box>
        ) : (
          <Box flexDirection="column">
            <Text>Actions</Text>
            {ACTIONS.map((action, index) => {
              const isSelected = index === selectedIndex;
              const isDisabled = action.requiresWallet && !wallet;
              const label = isSelected
                ? `> ${action.label}`
                : `  ${action.label}`;
              return (
                <Text
                  key={action.value}
                  color={isDisabled ? "gray" : isSelected ? "green" : undefined}
                >
                  {label}
                </Text>
              );
            })}
            <Text dimColor>Use Up/Down, Enter to run, q to quit</Text>
          </Box>
        )}
      </Box>

      {isBusy && (
        <Box marginTop={1}>
          <Text color="yellow">Working...</Text>
        </Box>
      )}

      <Box marginTop={1} flexDirection="column">
        <Text>Output</Text>
        {visibleLogs.length === 0 ? (
          <Text dimColor>No output yet.</Text>
        ) : (
          visibleLogs.map((line, index) => <Text key={index}>{line}</Text>)
        )}
      </Box>
    </Box>
  );
};

render(<App />);
