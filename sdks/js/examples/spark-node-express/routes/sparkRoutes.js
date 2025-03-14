import { Router } from "express";
import {
  loadMnemonic,
  saveMnemonic,
  formatTransferResponse,
} from "../utils/utils.js";
import { SparkWallet } from "@buildonspark/spark-sdk";
const SPARK_MNEMONIC_PATH = ".spark-mnemonic";
const wallet = new SparkWallet("REGTEST"); // or "MAINNET" for production

export const createSparkRouter = (wallet, mnemonicPath) => {
  const router = Router();
  // Get wallet
  router.get("/wallet", async (req, res) => {
    res.json(wallet);
  });

  /**
   * Initialize wallet
   * @route POST /wallet/init
   * @param {string} [mnemonicOrSeed] - The mnemonic or seed to initialize the wallet
   * @returns {Promise<{
   *   data: {
   *     message: string,
   *     mnemonic: string // only returned if mnemonicOrSeed is not provided
   *   }
   * }>}
   *
   * @example
   * // Request
   * {
   *   "mnemonicOrSeed": "bip39 mnemonic recovery phrase or seed phrase"
   * }
   *
   * // Response
   * {
   *   "data": {
   *     "message": "Wallet initialized",
   *     "mnemonic": "bip39 mnemonic recovery phrase" // only returned if mnemonicOrSeed is not provided
   *   }
   * }
   */
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
        data: {
          message: "Wallet initialized",
          ...response,
        },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Get wallet identity public key
   * @route GET /wallet/identity-public-key
   * @returns {Promise<{
   *   data: {
   *     identityPublicKey: string
   *   }
   * }>}
   *
   * @example
   * // Response
   * {
   *   "data": {
   *     "identityPublicKey": "0x1234567890abcdef"
   *   }
   * }
   */
  router.get("/wallet/identity-public-key", async (req, res) => {
    try {
      const identityPublicKey = await wallet.getIdentityPublicKey();
      res.json({
        data: { identityPublicKey },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Get wallet spark address
   * @route GET /wallet/spark-address
   * @returns {Promise<{
   *   data: {
   *     sparkAddress: string
   *   }
   * }>}
   *
   * @example
   * // Response
   * {
   *   "data": {
   *     "sparkAddress": "0123401234012340123401234"
   *   }
   * }
   */
  router.get("/wallet/spark-address", async (req, res) => {
    try {
      const sparkAddress = await wallet.getSparkAddress();
      res.json({
        data: { sparkAddress },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Get wallet balance
   * @route GET /wallet/balance
   * @returns {Promise<{
   *   data: {
   *     balance: string
   *     tokenBalances: {
   *       [tokenPublicKey: string]: {
   *         balance: string
   *       }
   *     }
   *   }
   * }>}
   *
   * @example
   * // Response
   * {
   *   "data": {
   *     "balance": "1000000000000000000",
   *     "tokenBalances": {
   *       "tokenPublicKey1": {
   *         "balance": "1000000000000000000"
   *       },
   *       "tokenPublicKey2": {
   *         "balance": "2000000000000000000"
   *       }
   *     }
   *   }
   * }
   */
  router.get("/wallet/balance", async (req, res) => {
    try {
      const balance = await wallet.getBalance(true);
      const tokenBalances = balance.tokenBalances
        ? Object.fromEntries(balance.tokenBalances)
        : {};

      res.json({
        data: {
          balance: balance.balance,
          tokenBalances,
        },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Get transfer history
   * @route GET /wallet/transfers
   * @param {number} [limit=20] - The number of transfers to return
   * @param {number} [offset=0] - The offset to start the transfers from
   * @returns {Promise<{
   *   data: {
   *     transfers: {
   *       id: string
   *       senderIdentityPublicKey: string // hex string of Uint8Array
   *       receiverIdentityPublicKey: string // hex string of Uint8Array
   *       status: string // mapped from TRANSFER_STATUS enum
   *       amount: string // bigint serialized to string
   *       expiryTime: string | null // ISO date string or null
   *       leaves: {
   *         leaf: {
   *           id: string
   *           treeId: string
   *           value: number
   *           parentNodeId?: string
   *           nodeTx: string // hex string of Uint8Array
   *           refundTx: string // hex string of Uint8Array
   *           vout: number
   *           verifyingPublicKey: string // hex string of Uint8Array
   *           ownerIdentityPublicKey: string // hex string of Uint8Array
   *           signingKeyshare: {
   *             ownerIdentifiers: string[]
   *             threshold: number
   *           }
   *           status: string
   *           network: string // mapped from NETWORK_MAP
   *         }
   *         secretCipher: string // hex string of Uint8Array
   *         signature: string // hex string of Uint8Array
   *         intermediateRefundTx: string // hex string of Uint8Array
   *       }[]
   *     }[]
   *     offset: number
   *   }
   * }>}
   *
   * @example
   * // Response
   * {
   *   "data": {
   *     "transfers": [{
   *       "id": "123",
   *       "senderIdentityPublicKey": "0x...",
   *       "receiverIdentityPublicKey": "0x...",
   *       "status": "COMPLETED",
   *       "amount": "1000000",
   *       "expiryTime": "2024-01-20T12:34:56.789Z",
   *       "leaves": [{
   *         "leaf": {
   *           "id": "leaf1",
   *           "value": 1000,
   *           // ... other leaf fields
   *         },
   *         "secretCipher": "0x...",
   *         "signature": "0x...",
   *         "intermediateRefundTx": "0x..."
   *       }]
   *     }],
   *     "offset": 0
   *   }
   * }
   */
  router.get("/wallet/transfers", async (req, res) => {
    try {
      const { limit = 20, offset = 0 } = req.query;
      const transfers = await wallet.getAllTransfers(
        Number(limit),
        Number(offset)
      );
      const transferResponse = transfers.transfers.map((transfer) =>
        formatTransferResponse(transfer)
      );
      res.json({
        data: {
          transfers: transferResponse,
          offset: transfers.offset,
        },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Send Spark Transfer
   * @route POST /spark/send-transfer
   * @param {string} receiverSparkAddress - The Spark address of the receiver
   * @param {string} amountSats - The amount to send in satoshis
   * @returns {Promise<{
   *   Promise<{
   *   data: {
   *     transfers: {
   *       id: string
   *       senderIdentityPublicKey: string // hex string of Uint8Array
   *       receiverIdentityPublicKey: string // hex string of Uint8Array
   *       status: string // mapped from TRANSFER_STATUS enum
   *       amount: string // bigint serialized to string
   *       expiryTime: string | null // ISO date string or null
   *       leaves: {
   *         leaf: {
   *           id: string
   *           treeId: string
   *           value: number
   *           parentNodeId?: string
   *           nodeTx: string // hex string of Uint8Array
   *           refundTx: string // hex string of Uint8Array
   *           vout: number
   *           verifyingPublicKey: string // hex string of Uint8Array
   *           ownerIdentityPublicKey: string // hex string of Uint8Array
   *           signingKeyshare: {
   *             ownerIdentifiers: string[]
   *             threshold: number
   *           }
   *           status: string
   *           network: string // mapped from NETWORK_MAP
   *         }
   *         secretCipher: string // hex string of Uint8Array
   *         signature: string // hex string of Uint8Array
   *         intermediateRefundTx: string // hex string of Uint8Array
   *       }
   *     }
   *   }
   * }>}
   *
   * @example
   * // Request
   * {
   *   "receiverSparkAddress": "0x...",
   *   "amountSats": "1000000"
   * }
   *
   * // Response
   * {
   *   "data": {
   *     "transfer": {
   *       "id": "123",
   *       "senderIdentityPublicKey": "0x...",
   *       "receiverIdentityPublicKey": "0x...",
   *       "amountSats": "1000000",
   *       "status": "COMPLETED",
   *       "expiryTime": "2024-01-20T12:34:56.789Z"
   *       "leaves": [{
   *         "leaf": {
   *           "id": "leaf1",
   *           "value": 1000,
   *           // ... other leaf fields
   *         },
   *       }]
   *     }
   *   }
   * }
   */
  router.post("/spark/send-transfer", async (req, res) => {
    try {
      const { receiverSparkAddress, amountSats } = req.body;
      const transfer = await wallet.sendSparkTransfer({
        receiverSparkAddress,
        amountSats: Number(amountSats),
      });
      const transferResponse = formatTransferResponse(transfer);
      res.json({
        data: { transfer: transferResponse },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Create lightning invoice
   * @route POST /lightning/create-invoice
   * @param {string} amountSats - The amount to create the invoice for in satoshis
   * @param {string} [memo] - The memo for the invoice
   * @param {number} [expirySeconds] - The expiry time for the invoice in seconds
   * @returns {Promise<{
   *   data: {
   *     invoice: string
   *   }
   * }>}
   */
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

  /**
   * Pay lightning invoice
   * @route POST /lightning/pay-invoice
   * @param {string} invoice - The invoice to pay
   * @returns {Promise<{
   *   data: {
   *     payment: {
   *       id: string
   *       createdAt: string
   *       updatedAt: string
   *       encodedInvoice: string
   *       fee: {
   *         originalValue: number
   *         originalUnit: string
   *         preferredCurrencyUnit: string
   *         preferredCurrencyValueRounded: number
   *         preferredCurrencyValueApprox: number
   *       }
   *       idempotencyKey: string
   *       status: string
   *       transfer: {
   *         id: string
   *         totalAmount: {
   *           originalValue: number
   *           originalUnit: string
   *           preferredCurrencyUnit: string
   *           preferredCurrencyValueRounded: number
   *           preferredCurrencyValueApprox: number
   *         }
   *       }
   *     }
   *   }
   * }>}
   */
  router.post("/lightning/pay-invoice", async (req, res) => {
    try {
      const { invoice } = req.body;
      const payment = await wallet.payLightningInvoice({ invoice });
      res.json({
        data: { payment },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Generate deposit address
   * @route GET /bitcoin/deposit-address
   * @returns {Promise<{
   *   data: {
   *     address: string
   *   }
   * }>}
   */
  router.get("/bitcoin/deposit-address", async (req, res) => {
    try {
      const address = await wallet.getDepositAddress();
      res.json({
        data: { address },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Withdraw to Bitcoin address
   * @route POST /bitcoin/withdraw
   * @param {string} onchainAddress - The Bitcoin address to withdraw to
   * @param {string} targetAmountSats - The amount to withdraw in satoshis
   * @returns {Promise<{
   *   data: {
   *     withdrawal: {
   *       id: string
   *       createdAt: string
   *       updatedAt: string
   *       fee: {
   *         originalValue: number
   *         originalUnit: string
   *         preferredCurrencyUnit: string
   *         preferredCurrencyValueRounded: number
   *         preferredCurrencyValueApprox: number
   *     }
   *     status: string
   *     expiresAt: string
   *     rawConnectorTransaction: string
   *     typename: string
   *   }
   * }>}
   */
  router.post("/bitcoin/withdraw", async (req, res) => {
    try {
      const { onchainAddress, targetAmountSats } = req.body;
      const withdrawal = await wallet.withdraw({
        onchainAddress,
        targetAmountSats,
      });
      res.json({
        data: { withdrawal },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Transfer tokens
   * @route POST /tokens/transfer
   * @param {string} tokenPublicKey - The public key of the token to transfer
   * @param {string} tokenAmount - The amount to transfer
   * @param {string} receiverSparkAddress - The Spark address of the receiver
   * @returns {Promise<{
   *   data: {
   *     transferTx: string
   *   }
   * }>}
   */
  router.post("/tokens/transfer", async (req, res) => {
    try {
      const { tokenPublicKey, tokenAmount, receiverSparkAddress } = req.body;
      const transferTx = await wallet.transferTokens({
        tokenPublicKey,
        tokenAmount,
        receiverSparkAddress,
      });
      res.json({
        data: { transferTx },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });

  /**
   * Withdraw tokens
   * @route POST /tokens/withdraw
   * @param {string} tokenPublicKey - The public key of the token to withdraw
   * @param {string} receiverPublicKey - The public key of the receiver
   * @param {string[]} leafIds - The IDs of the leaves to withdraw
   * @returns {Promise<{
   *   data: {
   *     withdrawal: string
   *   }
   * }>}
   */
  router.post("/tokens/withdraw", async (req, res) => {
    try {
      const { tokenPublicKey, receiverPublicKey, leafIds } = req.body;
      const withdrawalTx = await wallet.withdrawTokens(
        tokenPublicKey,
        receiverPublicKey,
        leafIds
      );
      res.json({
        data: { withdrawalTx },
      });
    } catch (error) {
      res.status(500).json({ error: error.message });
    }
  });
  return router;
};

export default createSparkRouter(wallet, SPARK_MNEMONIC_PATH);
