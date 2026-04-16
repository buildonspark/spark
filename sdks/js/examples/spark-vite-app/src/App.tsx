import "./App.css";
import * as spark from "@buildonspark/spark-sdk";
import {
  SparkWallet,
  type ConfigOptions,
  getSparkFrost,
  type DummyTx,
} from "@buildonspark/spark-sdk";
import { useState } from "react";
import {
  getExampleSparkNetwork,
  getLocalBitcoinRpcProxyPath,
  getExampleWalletOptions,
} from "./wallet-config.js";

type Network = "MAINNET" | "REGTEST" | "TESTNET";
type Target = "DEV" | "LOCAL" | "PROD";
type StatusType = "info" | "success" | "error";
type PrivateConfigMap = Partial<Record<Network, ConfigOptions>>;
type BitcoinRpcResponse<T> = {
  result: T;
  error: { message: string } | null;
};

declare const __SPARK_PRIVATE_CONFIGS__: {
  dev?: PrivateConfigMap;
};
declare const __SPARK_LOCAL_CONFIG_AVAILABLE__: boolean;

const PUBLIC_NETWORKS: readonly Network[] = ["MAINNET", "TESTNET", "REGTEST"];
const LOCALHOST_HOSTNAMES = new Set(["127.0.0.1", "::1", "localhost"]);
const IS_LOCALHOST = LOCALHOST_HOSTNAMES.has(window.location.hostname);
const HAS_LOCAL_CONFIG = IS_LOCALHOST && __SPARK_LOCAL_CONFIG_AVAILABLE__;
const PRIVATE_DEV_CONFIGS = getPrivateDevConfigs();
const HAS_PRIVATE_DEV_CONFIGS = PUBLIC_NETWORKS.some(
  (network) => PRIVATE_DEV_CONFIGS[network],
);
const DEFAULT_NETWORK = getDefaultNetwork();
const DEFAULT_TARGET = getDefaultTarget();
const BITCOIN_RPC_PROXY_PATH = getLocalBitcoinRpcProxyPath();

function configureBrowserBitcoinRpcProxy() {
  const globalScope = globalThis as typeof globalThis & {
    process?: unknown;
  };
  const processShim = globalScope.process as
    | {
        env?: Record<string, string | undefined>;
      }
    | undefined;

  if (!processShim) {
    (globalScope as { process?: unknown }).process = { env: {} };
  } else if (!processShim.env) {
    processShim.env = {};
  }

  const env = (
    globalScope.process as { env: Record<string, string | undefined> }
  ).env;
  env.BITCOIN_RPC_URL = new URL(
    BITCOIN_RPC_PROXY_PATH,
    window.location.origin,
  ).toString();
  env.BITCOIN_RPC_USER ??= "testutil";
  env.BITCOIN_RPC_PASSWORD ??= "testutilpassword";
}

function getPrivateDevConfigs(): PrivateConfigMap {
  return typeof __SPARK_PRIVATE_CONFIGS__ === "object" &&
    __SPARK_PRIVATE_CONFIGS__ !== null &&
    typeof __SPARK_PRIVATE_CONFIGS__.dev === "object" &&
    __SPARK_PRIVATE_CONFIGS__.dev !== null
    ? __SPARK_PRIVATE_CONFIGS__.dev
    : {};
}

function getDefaultNetwork(): Network {
  const configuredNetwork = getExampleSparkNetwork(import.meta.env, "MAINNET");

  if (configuredNetwork === "REGTEST" || configuredNetwork === "TESTNET") {
    return configuredNetwork;
  }

  return "MAINNET";
}

function getDefaultTarget(): Target {
  const configuredTarget = String(
    import.meta.env.VITE_SPARK_TARGET ?? "",
  ).toUpperCase();

  if (configuredTarget === "LOCAL" && HAS_LOCAL_CONFIG) {
    return "LOCAL";
  }

  if (configuredTarget === "DEV" && HAS_PRIVATE_DEV_CONFIGS) {
    return "DEV";
  }

  if (
    getExampleSparkNetwork(import.meta.env, "MAINNET") === "LOCAL" &&
    HAS_LOCAL_CONFIG
  ) {
    return "LOCAL";
  }

  return "PROD";
}

function getNetworksForTarget(target: Target): Network[] {
  if (target !== "DEV") {
    return [...PUBLIC_NETWORKS];
  }

  return PUBLIC_NETWORKS.filter((network) =>
    Boolean(PRIVATE_DEV_CONFIGS[network]),
  );
}

function getInitialNetwork(target: Target): Network {
  if (target === "LOCAL") {
    return "REGTEST";
  }

  const availableNetworks = getNetworksForTarget(target);
  return availableNetworks.includes(DEFAULT_NETWORK)
    ? DEFAULT_NETWORK
    : (availableNetworks[0] ?? "MAINNET");
}

function getWalletOptions(target: Target, network: Network): ConfigOptions {
  if (target === "LOCAL") {
    configureBrowserBitcoinRpcProxy();
    return {
      ...getExampleWalletOptions(
        import.meta.env,
        "LOCAL",
        window.location.origin,
      ),
      log: true,
    };
  }

  if (target === "DEV") {
    const config = PRIVATE_DEV_CONFIGS[network];
    if (!config) {
      throw new Error(`No DEV config available for ${network}`);
    }

    return {
      ...config,
      log: true,
    };
  }

  return {
    ...getExampleWalletOptions(
      import.meta.env,
      network,
      window.location.origin,
    ),
    log: true,
  };
}

async function bitcoinRpc<T>(method: string, params: unknown[]): Promise<T> {
  const response = await fetch(BITCOIN_RPC_PROXY_PATH, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      jsonrpc: "1.0",
      id: "spark-vite-app",
      method,
      params,
    }),
  });

  if (!response.ok) {
    throw new Error(`Bitcoin RPC HTTP error: ${response.status}`);
  }

  const data = (await response.json()) as BitcoinRpcResponse<T>;
  if (data.error) {
    throw new Error(`Bitcoin RPC error: ${data.error.message}`);
  }

  return data.result;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function satsToBitcoinAmount(amountSats: number): number {
  return Number((amountSats / 100_000_000).toFixed(8));
}

async function waitForBalanceAtLeast(
  wallet: SparkWallet,
  targetBalance: bigint,
  timeoutMs: number = 30_000,
  pollIntervalMs: number = 2_000,
): Promise<bigint | null> {
  const deadline = Date.now() + timeoutMs;

  while (Date.now() < deadline) {
    const { balance } = await wallet.getBalance();
    if (balance >= targetBalance) {
      return balance;
    }

    await sleep(pollIntervalMs);
  }

  return null;
}

async function claimDepositWithRetry(
  wallet: SparkWallet,
  txid: string,
  attempts: number = 20,
  delayMs: number = 2_000,
): Promise<bigint> {
  let lastError: unknown;

  for (let attempt = 0; attempt < attempts; attempt += 1) {
    try {
      const leaves = await wallet.claimDeposit(txid);
      return leaves.reduce((sum, leaf) => sum + BigInt(leaf.value), 0n);
    } catch (error) {
      lastError = error;
      if (attempt === attempts - 1) {
        break;
      }
      await sleep(delayMs);
    }
  }

  throw lastError;
}

function App() {
  const [status, setStatus] = useState<{ type: StatusType; message: string }>({
    type: "info",
    message: "Ready",
  });
  const [mnemonic, setMnemonic] = useState("");
  const [target, setTarget] = useState<Target>(DEFAULT_TARGET);
  const [network, setNetwork] = useState<Network>(() =>
    getInitialNetwork(DEFAULT_TARGET),
  );
  const [wallet, setWallet] = useState<SparkWallet | null>(null);
  const [walletTarget, setWalletTarget] = useState<Target | null>(null);
  const [sparkAddress, setSparkAddress] = useState<string | null>(null);
  const [balance, setBalance] = useState<string | null>(null);
  const [depositAddress, setDepositAddress] = useState<string | null>(null);
  const [depositAmount, setDepositAmount] = useState("50000");
  const [depositTxid, setDepositTxid] = useState("");
  const [depositActionPending, setDepositActionPending] = useState(false);
  const [recipientAddress, setRecipientAddress] = useState("");
  const [sendAmount, setSendAmount] = useState("");
  const [sendType, setSendType] = useState<"spark" | "lightning">("spark");
  const [maxFeeSats, setMaxFeeSats] = useState("100");
  const [dummyTx, setDummyTx] = useState<DummyTx | null>(null);
  const [invoiceAmount, setInvoiceAmount] = useState("");
  const [invoice, setInvoice] = useState<string | null>(null);
  const targetOptions = [
    "PROD",
    ...(HAS_PRIVATE_DEV_CONFIGS ? (["DEV"] as const) : []),
    ...(HAS_LOCAL_CONFIG ? (["LOCAL"] as const) : []),
  ] as const;
  const showTargetSelector = targetOptions.length > 1;
  const availableNetworks = getNetworksForTarget(target);

  const inferNetworkFromAddress = (
    address: string,
  ): { network?: Network; target?: Target } | null => {
    if (address.startsWith("sparkl") || address.startsWith("spl")) {
      return HAS_LOCAL_CONFIG ? { target: "LOCAL" } : null;
    }
    if (address.startsWith("sparkrt") || address.startsWith("sprt")) {
      return { network: "REGTEST" };
    }
    if (address.startsWith("sparkt") || address.startsWith("spt")) {
      return { network: "TESTNET" };
    }
    if (address.startsWith("spark") || address.startsWith("sp")) {
      return { network: "MAINNET" };
    }
    return null;
  };

  const handleTargetChange = (nextTarget: Target) => {
    setTarget(nextTarget);
    if (nextTarget === "LOCAL") {
      setNetwork("REGTEST");
      return;
    }

    const nextAvailableNetworks = getNetworksForTarget(nextTarget);
    if (!nextAvailableNetworks.includes(network)) {
      setNetwork(nextAvailableNetworks[0] ?? "MAINNET");
    }
  };

  const handleRecipientChange = (address: string) => {
    setRecipientAddress(address);
    const inferred = inferNetworkFromAddress(address);
    if (!inferred || wallet) {
      return;
    }

    if (inferred.target === "LOCAL" && target !== "LOCAL") {
      setTarget("LOCAL");
      setNetwork("REGTEST");
      setStatus({ type: "info", message: "Target set to LOCAL" });
      return;
    }

    if (inferred.network && inferred.network !== network) {
      if (target === "LOCAL") {
        setTarget("PROD");
      } else if (
        target === "DEV" &&
        !getNetworksForTarget("DEV").includes(inferred.network)
      ) {
        setTarget("PROD");
      }
      setNetwork(inferred.network);
      setStatus({
        type: "info",
        message: `Network set to ${inferred.network}`,
      });
    }
  };

  const testWasm = async () => {
    setStatus({ type: "info", message: "Testing WASM..." });
    try {
      const sparkFrost = getSparkFrost();
      const tx = await sparkFrost.createDummyTx(
        "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
        65536n,
      );
      setDummyTx(tx);
      setStatus({ type: "success", message: "WASM works!" });
    } catch (err) {
      setStatus({
        type: "error",
        message: `WASM error: ${err instanceof Error ? err.message : err}`,
      });
    }
  };

  const initializeWallet = async () => {
    if (!mnemonic.trim()) {
      setStatus({ type: "error", message: "Enter a mnemonic" });
      return;
    }
    setStatus({ type: "info", message: "Initializing..." });
    try {
      const selectedTarget = target;
      const selectedNetwork = network;
      const { wallet: w } = await SparkWallet.initialize({
        mnemonicOrSeed: mnemonic.trim(),
        options: getWalletOptions(selectedTarget, selectedNetwork),
      });
      setWallet(w);
      setWalletTarget(selectedTarget);
      setBalance(null);
      setDepositAddress(null);
      setDepositTxid("");
      setSparkAddress(await w.getSparkAddress());
      setStatus({ type: "success", message: "Wallet initialized!" });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    }
  };

  const generateWallet = async () => {
    setStatus({ type: "info", message: "Generating..." });
    try {
      const selectedTarget = target;
      const selectedNetwork = network;
      const { wallet: w, mnemonic: m } = await SparkWallet.initialize({
        options: getWalletOptions(selectedTarget, selectedNetwork),
      });
      setWallet(w);
      setWalletTarget(selectedTarget);
      if (m) setMnemonic(m);
      setBalance(null);
      setDepositAddress(null);
      setDepositTxid("");
      setSparkAddress(await w.getSparkAddress());
      setStatus({ type: "success", message: "Wallet generated!" });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    }
  };

  const refreshBalance = async () => {
    if (!wallet) return;
    setStatus({ type: "info", message: "Fetching balance..." });
    try {
      const { balance: b } = await wallet.getBalance();
      setBalance(b.toString());
      setStatus({ type: "success", message: `Balance: ${b} sats` });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    }
  };

  const createDepositAddress = async () => {
    if (!wallet) return;

    setDepositActionPending(true);
    setStatus({ type: "info", message: "Creating deposit address..." });
    try {
      const address = await wallet.getSingleUseDepositAddress();
      setDepositAddress(address);
      setDepositTxid("");
      setStatus({ type: "success", message: "Deposit address ready!" });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    } finally {
      setDepositActionPending(false);
    }
  };

  const claimDeposit = async () => {
    if (!wallet) return;

    const txid = depositTxid.trim();
    if (!txid) {
      setStatus({ type: "error", message: "Enter a deposit transaction ID" });
      return;
    }

    setDepositActionPending(true);
    setStatus({ type: "info", message: "Claiming deposit..." });
    try {
      const { balance: beforeBalance } = await wallet.getBalance();
      const claimedSats = await claimDepositWithRetry(wallet, txid);
      const settledBalance =
        claimedSats > 0n
          ? await waitForBalanceAtLeast(wallet, beforeBalance + claimedSats)
          : beforeBalance;

      if (settledBalance !== null) {
        setBalance(settledBalance.toString());
      }

      setStatus({
        type: "success",
        message:
          claimedSats > 0n
            ? `Claimed ${claimedSats} sats from deposit ${txid}`
            : `Deposit ${txid} claimed`,
      });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    } finally {
      setDepositActionPending(false);
    }
  };

  const fundLocally = async () => {
    if (!wallet) return;
    if (walletTarget !== "LOCAL") {
      setStatus({
        type: "error",
        message: "Fund Locally requires a wallet initialized with LOCAL target",
      });
      return;
    }
    if (!IS_LOCALHOST) {
      setStatus({
        type: "error",
        message:
          "Fund Locally is only available when the app is opened from localhost",
      });
      return;
    }

    const amountSats = Number.parseInt(depositAmount, 10);
    if (!Number.isFinite(amountSats) || amountSats <= 0) {
      setStatus({ type: "error", message: "Enter a valid deposit amount" });
      return;
    }

    setDepositActionPending(true);
    setStatus({ type: "info", message: "Funding local deposit..." });
    try {
      const depositConfirmationBlocks = 3;
      const { balance: beforeBalance } = await wallet.getBalance();
      const address = await wallet.getSingleUseDepositAddress();
      setDepositAddress(address);

      const txid = await bitcoinRpc<string>("sendtoaddress", [
        address,
        satsToBitcoinAmount(amountSats),
      ]);
      setDepositTxid(txid);

      const miningAddress = await bitcoinRpc<string>("getnewaddress", []);
      await bitcoinRpc("generatetoaddress", [
        depositConfirmationBlocks,
        miningAddress,
      ]);
      await sleep(3_000);

      const claimedSats = await claimDepositWithRetry(wallet, txid);
      const settledBalance =
        claimedSats > 0n
          ? await waitForBalanceAtLeast(wallet, beforeBalance + claimedSats)
          : beforeBalance;

      if (settledBalance !== null) {
        setBalance(settledBalance.toString());
      }

      setStatus({
        type: "success",
        message: `Funded and claimed ${claimedSats || BigInt(amountSats)} sats (${txid})`,
      });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    } finally {
      setDepositActionPending(false);
    }
  };

  const createInvoice = async () => {
    if (!wallet) return;
    const amount = parseInt(invoiceAmount, 10) || 0;
    setStatus({ type: "info", message: "Creating invoice..." });
    try {
      const result = await wallet.createLightningInvoice({
        amountSats: amount,
      });
      setInvoice(result.invoice.encodedInvoice);
      setStatus({ type: "success", message: "Invoice created!" });
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    }
  };

  const copyToClipboard = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setStatus({ type: "success", message: "Copied to clipboard!" });
    } catch {
      setStatus({ type: "error", message: "Failed to copy" });
    }
  };

  const sendTransaction = async () => {
    if (!wallet) return;
    if (!recipientAddress.trim()) {
      setStatus({
        type: "error",
        message: "Enter recipient address or invoice",
      });
      return;
    }

    setStatus({ type: "info", message: "Sending..." });
    try {
      if (sendType === "lightning") {
        const fee = parseInt(maxFeeSats, 10) || 100;
        const result = await wallet.payLightningInvoice({
          invoice: recipientAddress.trim(),
          maxFeeSats: fee,
        });
        const resultId = "id" in result ? result.id : "unknown";
        setStatus({ type: "success", message: `Paid! ID: ${resultId}` });
      } else {
        const amount = parseInt(sendAmount, 10);
        if (isNaN(amount) || amount <= 0) {
          setStatus({ type: "error", message: "Enter valid amount" });
          return;
        }
        const result = await wallet.transfer({
          receiverSparkAddress: recipientAddress.trim(),
          amountSats: amount,
        });
        setStatus({ type: "success", message: `Sent! ID: ${result.id}` });
      }
      refreshBalance();
    } catch (err) {
      setStatus({
        type: "error",
        message: `Error: ${err instanceof Error ? err.message : err}`,
      });
    }
  };

  return (
    <>
      <h1>Spark + Vite</h1>

      <div className={`status ${status.type}`}>{status.message}</div>

      <div className="card">
        {/* WASM Test */}
        <div className="section">
          <h3>1. Test WASM</h3>
          <button onClick={testWasm}>Test WASM Signing</button>
          {dummyTx && (
            <div
              className="code clickable"
              onClick={() => copyToClipboard(dummyTx.txid)}
            >
              txid: {dummyTx.txid}
            </div>
          )}
        </div>

        {/* Wallet Init */}
        <div className="section">
          <h3>2. Initialize Wallet</h3>
          {showTargetSelector && (
            <>
              <h3>Target</h3>
              <div className="toggle-row">
                {targetOptions.map((option) => (
                  <button
                    key={option}
                    onClick={() => handleTargetChange(option)}
                    className={target === option ? "active" : ""}
                  >
                    {option}
                  </button>
                ))}
              </div>
            </>
          )}
          <h3>Network</h3>
          <div className="toggle-row">
            {availableNetworks.map((option) => {
              const disabled = target === "LOCAL" && option !== "REGTEST";
              return (
                <button
                  key={option}
                  onClick={() => setNetwork(option)}
                  className={network === option ? "active" : ""}
                  disabled={disabled}
                >
                  {option}
                </button>
              );
            })}
          </div>
          <textarea
            placeholder="Enter 12 or 24 word mnemonic..."
            value={mnemonic}
            onChange={(e) => setMnemonic(e.target.value)}
            rows={3}
          />
          <div className="button-row">
            <button onClick={initializeWallet}>Load Wallet</button>
            <button onClick={generateWallet}>Generate New</button>
          </div>
        </div>

        {/* Wallet Info */}
        {wallet && (
          <>
            <div className="section">
              <h3>3. Wallet Info</h3>
              {sparkAddress && (
                <div
                  className="code clickable"
                  onClick={() => copyToClipboard(sparkAddress)}
                >
                  {sparkAddress}
                </div>
              )}
              {balance !== null && (
                <div className="balance-display">
                  <span className="balance-amount">
                    {Number(balance).toLocaleString()}
                  </span>
                  <span className="balance-unit">sats</span>
                </div>
              )}
              <div className="button-row">
                <button onClick={refreshBalance}>Refresh Balance</button>
              </div>
            </div>

            {/* Deposit */}
            <div className="section">
              <h3>4. Deposit</h3>
              <div className="button-row">
                <button
                  onClick={createDepositAddress}
                  disabled={depositActionPending}
                >
                  Get Deposit Address
                </button>
              </div>
              {depositAddress && (
                <div
                  className="code clickable"
                  onClick={() => copyToClipboard(depositAddress)}
                >
                  {depositAddress}
                </div>
              )}
              {walletTarget === "LOCAL" && IS_LOCALHOST && (
                <div className="button-row deposit-action-row">
                  <input
                    type="number"
                    placeholder="Amount (sats)"
                    value={depositAmount}
                    onChange={(e) => setDepositAmount(e.target.value)}
                    disabled={depositActionPending}
                  />
                  <button onClick={fundLocally} disabled={depositActionPending}>
                    Fund Locally
                  </button>
                </div>
              )}
              <div className="button-row deposit-action-row">
                <input
                  type="text"
                  placeholder="Deposit transaction ID"
                  value={depositTxid}
                  onChange={(e) => setDepositTxid(e.target.value)}
                  disabled={depositActionPending}
                />
                <button
                  onClick={() => claimDeposit()}
                  disabled={depositActionPending}
                >
                  Claim Deposit
                </button>
              </div>
            </div>

            {/* Receive */}
            <div className="section">
              <h3>5. Receive</h3>
              <div className="button-row" style={{ marginBottom: "12px" }}>
                <input
                  type="number"
                  placeholder="Amount (sats)"
                  value={invoiceAmount}
                  onChange={(e) => setInvoiceAmount(e.target.value)}
                  style={{ flex: 1, marginBottom: 0 }}
                />
                <button onClick={createInvoice} style={{ marginBottom: 0 }}>
                  Create Invoice
                </button>
              </div>
              {invoice && (
                <div
                  className="code clickable"
                  onClick={() => copyToClipboard(invoice)}
                >
                  {invoice}
                </div>
              )}
            </div>

            {/* Send */}
            <div className="section">
              <h3>6. Send</h3>
              <div className="toggle-row">
                <button
                  onClick={() => setSendType("spark")}
                  className={sendType === "spark" ? "active" : ""}
                >
                  Spark
                </button>
                <button
                  onClick={() => setSendType("lightning")}
                  className={sendType === "lightning" ? "active" : ""}
                >
                  Lightning
                </button>
              </div>
              <input
                type="text"
                placeholder={
                  sendType === "lightning"
                    ? "Lightning invoice (lnbc...)"
                    : "Recipient Spark address"
                }
                value={recipientAddress}
                onChange={(e) => handleRecipientChange(e.target.value)}
              />
              {sendType === "spark" ? (
                <input
                  type="number"
                  placeholder="Amount (sats)"
                  value={sendAmount}
                  onChange={(e) => setSendAmount(e.target.value)}
                />
              ) : (
                <input
                  type="number"
                  placeholder="Max fee (sats)"
                  value={maxFeeSats}
                  onChange={(e) => setMaxFeeSats(e.target.value)}
                />
              )}
              <button onClick={sendTransaction}>
                {sendType === "lightning" ? "Pay Invoice" : "Send"}
              </button>
            </div>
          </>
        )}
      </div>
    </>
  );
}

export default App;

/* For debugging purposes only, this is not required for the SDK to work: */
interface SparkWindow extends Window {
  s: typeof spark;
}
declare let window: SparkWindow;
window.s = spark;
