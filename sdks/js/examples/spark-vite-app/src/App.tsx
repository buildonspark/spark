import "./App.css";
import * as spark from "@buildonspark/spark-sdk";
import {
  SparkWallet,
  getSparkFrost,
  type DummyTx,
} from "@buildonspark/spark-sdk";
import { useState } from "react";

type Network = "MAINNET" | "TESTNET" | "REGTEST";
type StatusType = "info" | "success" | "error";

function App() {
  const [status, setStatus] = useState<{ type: StatusType; message: string }>({
    type: "info",
    message: "Ready",
  });
  const [mnemonic, setMnemonic] = useState("");
  const [network, setNetwork] = useState<Network>("MAINNET");
  const [wallet, setWallet] = useState<SparkWallet | null>(null);
  const [sparkAddress, setSparkAddress] = useState<string | null>(null);
  const [balance, setBalance] = useState<string | null>(null);
  const [recipientAddress, setRecipientAddress] = useState("");
  const [sendAmount, setSendAmount] = useState("");
  const [sendType, setSendType] = useState<"spark" | "lightning">("spark");
  const [maxFeeSats, setMaxFeeSats] = useState("100");
  const [dummyTx, setDummyTx] = useState<DummyTx | null>(null);
  const [invoiceAmount, setInvoiceAmount] = useState("");
  const [invoice, setInvoice] = useState<string | null>(null);

  const inferNetworkFromAddress = (address: string): Network | null => {
    if (address.startsWith("sparkrt") || address.startsWith("sprt"))
      return "REGTEST";
    if (address.startsWith("sparkt") || address.startsWith("spt"))
      return "TESTNET";
    if (address.startsWith("spark") || address.startsWith("sp"))
      return "MAINNET";
    return null;
  };

  const handleRecipientChange = (address: string) => {
    setRecipientAddress(address);
    const inferred = inferNetworkFromAddress(address);
    if (inferred && inferred !== network && !wallet) {
      setNetwork(inferred);
      setStatus({ type: "info", message: `Network set to ${inferred}` });
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
      const { wallet: w } = await SparkWallet.initialize({
        mnemonicOrSeed: mnemonic.trim(),
        options: { network },
      });
      setWallet(w);
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
      const { wallet: w, mnemonic: m } = await SparkWallet.initialize({
        options: { network },
      });
      setWallet(w);
      if (m) setMnemonic(m);
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
          <div className="network-selector">
            {(["MAINNET", "TESTNET", "REGTEST"] as const).map((n) => (
              <button
                key={n}
                onClick={() => setNetwork(n)}
                className={network === n ? "active" : ""}
              >
                {n}
              </button>
            ))}
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

            {/* Receive */}
            <div className="section">
              <h3>4. Receive</h3>
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
              <h3>5. Send</h3>
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
