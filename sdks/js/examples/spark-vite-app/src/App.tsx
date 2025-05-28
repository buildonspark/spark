import { SparkWallet, createDummyTx } from "@buildonspark/spark-sdk";
import { useState } from "react";

function App() {
  const [sparkWallet, setSparkWallet] = useState<SparkWallet | null>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [balance, setBalance] = useState<number>(0);

  const initializeSpark = async () => {
    try {
      const { wallet } = await SparkWallet.initialize({
        options: {
          network: "REGTEST",
        },
      });
      setSparkWallet(wallet);
      console.log("Spark client initialized successfully!");
    } catch (error) {
      console.error("Failed to initialize Spark client:", error);
    }
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

  const dummyTx = createDummyTx({
    address: "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
    amountSats: 65536n,
  });

  return (
    <div className="App">
      <h1>Vite + React + Spark SDK</h1>
      <div className="card">
        <p>Test transaction ID</p>
        <p>{dummyTx.txid}</p>
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
