import * as spark from "@buildonspark/spark-sdk";
import {
  SparkWallet,
  getSparkFrost,
  type DummyTx,
} from "@buildonspark/spark-sdk";
import { useEffect, useState } from "react";

function App() {
  const [sparkWallet, setSparkWallet] = useState<SparkWallet | null>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [balance, setBalance] = useState<number>(0);
  const [dummyTx, setDummyTx] = useState<DummyTx | null>(null);

  const initializeSpark = async () => {
    const { wallet } = await SparkWallet.initialize({
      options: {
        network: "REGTEST",
      },
    });
    setSparkWallet(wallet);
    console.log("Spark client initialized successfully!");
  };

  const createInvoice = async () => {
    if (!sparkWallet) {
      console.error("Spark client not initialized");
      return;
    }
    const invoice = await sparkWallet.createLightningInvoice({
      amountSats: 100,
    });
    setInvoice(invoice.invoice.encodedInvoice);
  };

  const getBalance = async () => {
    if (!sparkWallet) {
      console.error("Spark client not initialized");
      return;
    }
    const balance = await sparkWallet.getBalance();
    setBalance(Number(balance.balance));
  };

  useEffect(() => {
    (async () => {
      const sparkFrost = getSparkFrost();
      const dummyTx = await sparkFrost.createDummyTx(
        "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
        65536n,
      );
      setDummyTx(dummyTx);
    })();
  }, []);

  return (
    <div className="App">
      <h1>Vite + React + Spark SDK</h1>
      <div className="card">
        <p>Test transaction ID</p>
        <p>{dummyTx ? dummyTx.txid : "Loading..."}</p>
        <button onClick={initializeSpark}>Initialize Spark Client</button>
        <p>
          {sparkWallet
            ? "Spark client is initialized!"
            : "Click the button to initialize Spark client"}
        </p>
        <button onClick={createInvoice}>Create Invoice</button>
        <p>Invoice: {invoice}</p>
        <button onClick={getBalance}>Get Balance</button>
        <p>Balance: {balance}</p>
      </div>
    </div>
  );
}

export default App;

interface SparkWindow extends Window {
  s: typeof spark;
}

declare let window: SparkWindow;

/* For debugging purposes only, not required: */
window.s = spark;
