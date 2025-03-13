import { Router } from "express";
import { loadMnemonic, saveMnemonic } from "../utils/utils.js";
import { SparkWallet } from "@buildonspark/spark-sdk";

const SPARK_MNEMONIC_PATH = ".spark-mnemonic";
const wallet = new SparkWallet("REGTEST"); // or "MAINNET" for production

export const createSparkRouter = (wallet, mnemonicPath) => {
  const router = Router();
  // Get wallet
  router.get("/wallet", async (req, res) => {
    res.json(wallet);
  });

  // Endpoint to initialize wallet
  router.post("/wallet/init", async (req, res) => {
    try {
      let { mnemonicOrSeed } = req.body;
      if (!mnemonicOrSeed) {
        mnemonicOrSeed = await loadMnemonic(mnemonicPath);
      }
      const response = await wallet.initWallet(mnemonicOrSeed || undefined);
      if (!mnemonicOrSeed && response.mnemonic) {
        await saveMnemonic(mnemonicPath, response.mnemonic);
      }
      res.json({
        message: "Wallet initialized",
        ...response,
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Get wallet identity public key
  router.get("/wallet/identity-public-key", async (req, res) => {
    const identityPublicKey = await wallet.getIdentityPublicKey();
    res.json({ identityPublicKey });
  });

  // Get wallet spark address
  router.get("/wallet/spark-address", async (req, res) => {
    const sparkAddress = await wallet.getSparkAddress();
    res.json({ sparkAddress });
  });

  // Get wallet balance
  router.get("/wallet/balance", async (req, res) => {
    try {
      // passing true to getBalance syncs the wallet and claims pending transfers
      const balance = await wallet.getBalance(true);
      res.json(balance);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Get transfer history
  router.get("/wallet/transfers", async (req, res) => {
    try {
      const { limit = 20, offset = 0 } = req.query;
      const transfers = await wallet.getAllTransfers(
        Number(limit),
        Number(offset)
      );
      res.json(transfers);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Send Spark transfer
  router.post("/spark/send-transfer", async (req, res) => {
    try {
      const { receiverSparkAddress, amountSats } = req.body;
      const transfer = await wallet.sendSparkTransfer({
        receiverSparkAddress,
        amountSats: Number(amountSats),
      });
      res.json(transfer);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Create lightning invoice
  router.post("/lightning/create-invoice", async (req, res) => {
    try {
      const { amountSats, memo, expirySeconds } = req.body;
      const invoice = await wallet.createLightningInvoice({
        amountSats: Number(amountSats),
        memo,
        expirySeconds: Number(expirySeconds),
      });
      res.json({ invoice });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Pay lightning invoice
  router.post("/lightning/pay-invoice", async (req, res) => {
    try {
      const { invoice } = req.body;
      const payment = await wallet.payLightningInvoice({ invoice });
      res.json(payment);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Generate deposit address
  router.get("/bitcoin/deposit-address", async (req, res) => {
    try {
      const address = await wallet.getDepositAddress();
      res.json({ address });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Withdraw to Bitcoin address
  router.post("/bitcoin/withdraw", async (req, res) => {
    try {
      const { onchainAddress, targetAmountSats } = req.body;
      const withdrawal = await wallet.withdraw({
        onchainAddress,
        targetAmountSats,
      });
      res.json(withdrawal);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Transfer tokens
  router.post("/tokens/transfer", async (req, res) => {
    try {
      const { tokenPublicKey, tokenAmount, receiverSparkAddress } = req.body;
      const transfer = await wallet.transferTokens({
        tokenPublicKey,
        tokenAmount,
        receiverSparkAddress,
      });
      res.json(transfer);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  // Withdraw tokens
  router.post("/tokens/withdraw", async (req, res) => {
    try {
      const { tokenPublicKey, receiverPublicKey, leafIds } = req.body;
      const withdrawal = await wallet.withdrawTokens(
        tokenPublicKey,
        receiverPublicKey,
        leafIds
      );
      res.json(withdrawal);
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });
  return router;
};

export default createSparkRouter(wallet, SPARK_MNEMONIC_PATH);
