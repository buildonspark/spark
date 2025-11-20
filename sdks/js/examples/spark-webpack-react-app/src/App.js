import * as spark from "@buildonspark/spark-sdk";
import { SparkWallet, getSparkFrost } from "@buildonspark/spark-sdk";
import React, { useState, useEffect } from "react";

function App() {
  const [sparkWallet, setSparkWallet] = useState(null);
  const [invoice, setInvoice] = useState(null);
  const [balance, setBalance] = useState(0);
  const [dummyTx, setDummyTx] = useState(null);

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
      <h1>Webpack + React + Spark SDK</h1>
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
        <p className="invoice-text">Invoice: {invoice}</p>
        <button onClick={getBalance}>Get Balance</button>
        <p>Balance: {balance}</p>
      </div>
    </div>
  );
}

export default App;

/* For debugging purposes only, not required: */
window.s = spark;
