import {
  ReactNativeSparkSigner,
  SparkWallet,
} from "@buildonspark/spark-sdk/native";
import { createDummyTx } from "@buildonspark/spark-sdk/native/spark-frost";
import { useEffect, useState } from "react";
import { Button, Text, View } from "react-native";

export default function Index() {
  const [balance, setBalance] = useState(0);
  const [balance1, setBalance1] = useState(0);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [wallet, setWallet] = useState<SparkWallet | null>(null);
  const [wallet1, setWallet1] = useState<SparkWallet | null>(null);
  const [dummyTx, setDummyTx] = useState("");

  useEffect(() => {
    if (wallet) {
      console.log("Setting up event listener for wallet");

      const handleTransferClaimed = (
        transferId: string,
        updatedBalance: number,
      ) => {
        console.log(
          "Transfer claimed event received!",
          transferId,
          updatedBalance,
        );
        setBalance(updatedBalance);
      };

      // Log all events to see what's happening
      wallet.on("transfer:claimed", handleTransferClaimed);
      wallet.on("*", (eventName: string, ...args: any[]) => {
        console.log("Wallet event received:", eventName, args);
      });

      return () => {
        console.log("Cleaning up event listeners");
        wallet.removeListener("transfer:claimed", handleTransferClaimed);
      };
    }
  }, [wallet]);

  useEffect(() => {
    if (wallet1) {
      console.log("Setting up event listener for wallet1");

      const handleTransferClaimed = (
        transferId: string,
        updatedBalance: number,
      ) => {
        console.log(
          "Transfer claimed event received!",
          transferId,
          updatedBalance,
        );
        setBalance1(updatedBalance);
      };

      // Add listener for transfer:claimed
      wallet1.on("transfer:claimed", handleTransferClaimed);

      // Add listener for all events to debug
      const handleAllEvents = (eventName: string, ...args: any[]) => {
        console.log("Wallet event received:", eventName, args);
      };
      wallet1.on("*", handleAllEvents);

      return () => {
        console.log("Cleaning up event listeners");
        wallet1.removeListener("transfer:claimed", handleTransferClaimed);
        wallet1.removeListener("*", handleAllEvents);
      };
    }
  }, [wallet1]);

  useEffect(() => {
    (async () => {
      const dummyTx = await createDummyTx(
        "bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te",
        65536n,
      );
      setDummyTx(dummyTx.txid);
    })();
  }, []);

  const initializeWallet = async () => {
    try {
      console.log("Initializing wallet");
      const wallet = await SparkWallet.initialize({
        mnemonicOrSeed:
          "hobby december demise nephew project twice expire zoo impact dinosaur domain student",
        options: {
          network: "REGTEST",
        },
        signer: new ReactNativeSparkSigner(),
      });
      const balance = await wallet.wallet.getBalance();
      console.log("Balance from wallet:", balance);

      // Update all states in a single batch
      setWallet(wallet.wallet);
      setBalance(Number(balance.balance));

      const wallet1 = await SparkWallet.initialize({
        mnemonicOrSeed:
          "hill actress apology mean barely limit unit party shallow begin prison either",
        options: {
          network: "REGTEST",
        },
        signer: new ReactNativeSparkSigner(),
      });

      const balance1 = await wallet1.wallet.getBalance();
      setWallet1(wallet1.wallet);
      setBalance1(Number(balance1.balance));
    } catch (error) {
      console.error("Error initializing wallet:", error);
    }
  };

  const createInvoice = async () => {
    if (!wallet) {
      console.error("Wallet not initialized");
      return;
    }
    console.log("Creating invoice");
    const invoice = await wallet.createLightningInvoice({
      amountSats: 100,
    });
    console.log("Invoice created", invoice);
    setInvoice(invoice.invoice.encodedInvoice);
  };

  const transfer = async () => {
    if (!wallet || !wallet1) {
      console.error("Wallet not initialized");
      return;
    }
    console.log("Transferring");
    const sparkAddress = await wallet1.getSparkAddress();
    console.log("Spark address", sparkAddress);
    const transfer = await wallet.transfer({
      amountSats: 100,
      receiverSparkAddress: sparkAddress,
    });
    console.log("Transferred", transfer);
  };

  const transfer1 = async () => {
    if (!wallet1 || !wallet) {
      console.error("Wallet not initialized");
      return;
    }
    console.log("Transferring");
    const transfer = await wallet1.transfer({
      amountSats: 100,
      receiverSparkAddress: await wallet.getSparkAddress(),
    });
    console.log("Transferred", transfer);
  };

  console.log("Balance", balance);
  return (
    <View
      style={{
        flex: 1,
        justifyContent: "center",
        alignItems: "center",
        flexDirection: "column",
      }}
    >
      <View
        style={{
          justifyContent: "center",
          alignItems: "center",
          flexDirection: "row",
        }}
      >
        <View
          style={{
            flex: 1,
            justifyContent: "center",
            alignItems: "center",
          }}
        >
          <Text style={{ marginBottom: 20 }}>Balance: {balance}</Text>
          <View style={{ marginBottom: 20 }}>
            <Button
              title="Initialize Wallet"
              onPress={() => {
                initializeWallet();
              }}
            />
          </View>
          <Button
            title="Create Invoice"
            onPress={() => {
              createInvoice();
            }}
          />
          <Text style={{ marginTop: 20 }}>Invoice: {invoice}</Text>
          <Button
            title="Transfer"
            onPress={() => {
              transfer();
            }}
          />
        </View>
        <View
          style={{
            flex: 1,
            justifyContent: "center",
            alignItems: "center",
          }}
        >
          <Text style={{ marginBottom: 20 }}>Balance: {balance1}</Text>
          <View style={{ marginBottom: 20 }}>
            <Button
              title="Initialize Wallet"
              onPress={() => {
                initializeWallet();
              }}
            />
          </View>
          <Button
            title="Create Invoice"
            onPress={() => {
              createInvoice();
            }}
          />
          <Text style={{ marginTop: 20 }}>Invoice: {invoice}</Text>
          <Button
            title="Transfer"
            onPress={() => {
              transfer1();
            }}
          />
        </View>
      </View>
      <View
        style={{
          marginTop: 30,
          justifyContent: "center",
          alignItems: "center",
          width: "80%",
        }}
      >
        <Text style={{ marginBottom: 15 }}>Dummy Transaction ID</Text>
        <Text>{dummyTx}</Text>
      </View>
    </View>
  );
}
