"use client";

import * as spark from "@buildonspark/spark-sdk";
import {
  SparkWallet,
  getSparkFrost,
  type DummyTx,
} from "@buildonspark/spark-sdk";
import { useEffect, useState } from "react";
import Image from "next/image";

interface SparkWindow extends Window {
  s: typeof spark;
}

declare let window: SparkWindow;

export default function Home() {
  const [sparkWallet, setSparkWallet] = useState<SparkWallet | null>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [balance, setBalance] = useState<number>(0);
  const [dummyTx, setDummyTx] = useState<DummyTx | null>(null);

  const initializeSpark = async () => {
    const { wallet } = await SparkWallet.initialize({
      options: {
        network: "REGTEST", // Or your desired network
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
    try {
      const inv = await sparkWallet.createLightningInvoice({
        amountSats: 100, // Example amount
      });
      setInvoice(inv.invoice.encodedInvoice);
      console.log("Invoice created:", inv.invoice.encodedInvoice);
    } catch (error) {
      console.error("Failed to create invoice:", error);
    }
  };

  const getBalance = async () => {
    if (!sparkWallet) {
      console.error("Spark client not initialized");
      return;
    }
    try {
      const bal = await sparkWallet.getBalance();
      setBalance(Number(bal.balance));
      console.log("Balance fetched:", bal.balance);
    } catch (error) {
      console.error("Failed to get balance:", error);
    }
  };

  useEffect(() => {
    /* For debugging purposes only, not required: */
    window.s = spark;
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
    <main className="flex min-h-screen flex-col items-center justify-start p-12">
      <h1 className="text-4xl font-bold mb-8">Next.js + Spark SDK</h1>

      <div className="bg-gray-800 shadow-md rounded-lg p-6 w-full max-w-2xl text-white">
        <div className="mb-6">
          <h2 className="text-2xl font-semibold mb-2">Dummy Transaction</h2>
          <p className="text-sm break-all">
            Test Transaction ID: {dummyTx ? dummyTx.txid : "Loading..."}
          </p>
        </div>

        <div className="mb-6">
          <button
            onClick={initializeSpark}
            className="bg-blue-500 hover:bg-blue-700 text-white font-bold py-2 px-4 rounded mb-2 w-full"
          >
            Initialize Spark Client
          </button>
          <p className="text-sm">
            {sparkWallet
              ? "Spark client is initialized!"
              : "Click the button to initialize Spark client"}
          </p>
        </div>

        <div className="mb-6">
          <button
            onClick={createInvoice}
            disabled={!sparkWallet}
            className="bg-green-500 hover:bg-green-700 text-white font-bold py-2 px-4 rounded mb-2 w-full disabled:opacity-50"
          >
            Create Invoice (100 sats)
          </button>
          {invoice && <p className="text-sm break-all">Invoice: {invoice}</p>}
        </div>

        <div>
          <button
            onClick={getBalance}
            disabled={!sparkWallet}
            className="bg-purple-500 hover:bg-purple-700 text-white font-bold py-2 px-4 rounded mb-2 w-full disabled:opacity-50"
          >
            Get Balance
          </button>
          {sparkWallet && ( // Only show balance info if wallet is initialized
            <p className="text-sm">Balance: {balance} sats</p>
          )}
        </div>
      </div>

      {/* Optional: Keep Vercel/Next.js branding if desired */}
      <div className="absolute bottom-8 flex flex-col items-center">
        <div className="flex items-center gap-2 mb-2">
          <span className="text-sm text-gray-400">Powered by</span>
          <a
            href="https://nextjs.org?utm_source=create-next-app&utm_medium=appdir-template&utm_campaign=create-next-app"
            target="_blank"
            rel="noopener noreferrer"
          >
            <Image
              src="/next.svg"
              alt="Next.js Logo"
              className="dark:invert"
              width={90}
              height={18}
              priority
            />
          </a>
        </div>
        <a
          href="https://vercel.com?utm_source=create-next-app&utm_medium=appdir-template&utm_campaign=create-next-app"
          target="_blank"
          rel="noopener noreferrer"
          className="flex items-center"
        >
          <span className="text-sm text-gray-400 mr-1">Deployed with</span>
          <Image
            src="/vercel.svg"
            alt="Vercel Logo"
            className="dark:invert"
            width={70}
            height={17}
            priority
          />
        </a>
      </div>
    </main>
  );
}
