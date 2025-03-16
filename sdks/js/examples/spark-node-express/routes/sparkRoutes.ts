import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { SparkWallet } from "@buildonspark/spark-sdk";
import { SparkProto } from "@buildonspark/spark-sdk/types";
import { isError } from "@lightsparkdev/core";
import { NextFunction, Router, Response, Request } from "express";
import {
  formatTransferResponse,
  loadMnemonic,
  saveMnemonic,
} from "../utils/utils.js";
import { BITCOIN_NETWORK } from "../src/index.js";

const SPARK_MNEMONIC_PATH = ".spark-mnemonic";

export const createSparkRouter = (
  walletClass: typeof SparkWallet | typeof IssuerSparkWallet,
  mnemonicPath: string
): {
  router: Router;
  getWallet: () => SparkWallet | IssuerSparkWallet | undefined;
  checkWalletInitialized: (
    req: Request,
    res: Response,
    next: NextFunction
  ) => void;
} => {
  const router: Router = Router();

  let walletInstance: SparkWallet | IssuerSparkWallet | undefined = undefined;

  const initWallet = async (mnemonicOrSeed: string) => {
    let res:
      | {
          mnemonic?: string | null;
          wallet: SparkWallet | IssuerSparkWallet;
        }
      | undefined = undefined;
    if (!walletInstance) {
      res = await walletClass.create({
        mnemonicOrSeed: mnemonicOrSeed,
        options: {
          network: BITCOIN_NETWORK,
        },
      });
      walletInstance = res?.wallet;
    }
    return res;
  };

  const getWallet = (): SparkWallet | IssuerSparkWallet | undefined => {
    if (!walletInstance) {
      console.error("Wallet not initialized");
      return undefined;
    }
    return walletInstance;
  };

  const checkWalletInitialized = (
    req: Request,
    res: Response,
    next: NextFunction
  ): void => {
    const wallet = getWallet();
    if (!wallet) {
      res.status(400).json({
        error: "Wallet not initialized. Please initialize the wallet first.",
      });
      return;
    }
    next();
  };

  // Get wallet
  router.get("/wallet", checkWalletInitialized, async (req, res) => {
    res.json(getWallet());
  });

  /**
   * Initialize wallet
   * @route POST /wallet/init
   * @param {string} [mnemonicOrSeed]
   *  - The mnemonic or seed to initialize the wallet.
   *      - If not provided:
   *        - If you have a mnemonic saved in the file system, it will be used.
   *        - Otherwise:
   *          - The wallet will be initialized with a random mnemonic.
   *          - The mnemonic will be saved to the file system.
   *          - The mnemonic will be returned in the response.
   *      - If provided:
   *        - The wallet will be initialized with the provided mnemonic or seed.
   *        - The mnemonic or seed will not be saved to the file system.
   * @returns {Promise<{
   *   data: {
   *     message: string,
   *     mnemonic: string // only returned if mnemonicOrSeed is not provided
   *   }
   * }>}
   */
  router.post("/wallet/init", async (req, res) => {
    try {
      let { mnemonicOrSeed } = req.body as { mnemonicOrSeed?: string | null };
      if (!mnemonicOrSeed) {
        mnemonicOrSeed = await loadMnemonic(mnemonicPath);
      }
      const response = await initWallet(mnemonicOrSeed ?? "");
      if (!mnemonicOrSeed && response?.mnemonic) {
        await saveMnemonic(mnemonicPath, response.mnemonic);
      }
      res.json({
        data: {
          message: "Wallet initialized",
          ...response,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
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
   */
  router.get(
    "/wallet/identity-public-key",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const identityPublicKey = await wallet!.getIdentityPublicKey();
        res.json({
          data: { identityPublicKey },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Get wallet spark address
   * @route GET /wallet/spark-address
   * @returns {Promise<{
   *   data: {
   *     sparkAddress: string
   *   }
   * }>}
   */
  router.get(
    "/wallet/spark-address",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const sparkAddress = await wallet!.getSparkAddress();
        res.json({
          data: { sparkAddress },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Get wallet balance
   * @route GET /wallet/balance
   * @returns {Promise<{
   *   data: {
   *     balance: string
   *     tokenBalances: {
   *       [tokenPublicKey: string]: {
   *         balance: string // BigInt converted to string in middleware
   *       }
   *     }
   *   }
   * }>}
   */
  router.get("/wallet/balance", checkWalletInitialized, async (req, res) => {
    const wallet = getWallet();
    try {
      const balance = await wallet!.getBalance();
      const tokenBalances: Record<string, { balance: BigInt }> =
        balance.tokenBalances
          ? Object.fromEntries(
              [...balance.tokenBalances].map(([key, value]) => [
                key,
                { balance: value.balance },
              ])
            )
          : {};

      res.json({
        data: {
          balance: balance.balance,
          tokenBalances,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });

  /**
   * Get transfer history
   * @route GET /wallet/transfers
   * @param {number} [limit=20] - The number of transfers to return
   * @param {number} [offset=0] - The offset to start the transfers from
   * @returns {Promise<{
   *   data: {
   *     transfers: SparkProto.Transfer[]
   *     offset: number
   *   }
   * }>}
   */
  router.get("/wallet/transfers", checkWalletInitialized, async (req, res) => {
    const wallet = getWallet();
    try {
      const { limit = 20, offset = 0 } = req.query as {
        limit?: number | undefined;
        offset?: number | undefined;
      };
      const transfers = await wallet!.getTransfers(
        Number(limit),
        Number(offset)
      );
      const transferResponse = transfers.transfers.map(
        (transfer: SparkProto.Transfer) => formatTransferResponse(transfer)
      );
      res.json({
        data: {
          transfers: transferResponse,
          offset: transfers.offset,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });

  /**
   * Get pending transfers
   * @route GET /wallet/pending-transfers
   * @returns {Promise<{
   *   data: {
   *     pendingTransfers: SparkProto.Transfer[]
   *   }
   * }>}
   */
  router.get(
    "/wallet/pending-transfers",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const pendingTransfers = await wallet!.getPendingTransfers();
        const transferResponse = pendingTransfers.map(
          (transfer: SparkProto.Transfer) => formatTransferResponse(transfer)
        );
        res.json({
          data: { pendingTransfers: transferResponse },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Claim all pending transfers
   * @route POST /wallet/claim-transfers
   * @returns {Promise<{
   *   data: {
   *     message: boolean
   * }>}
   */
  router.post(
    "/wallet/claim-transfers",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const message = await wallet!.claimTransfers();
        res.json({
          data: { message },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Send Spark Transfer
   * @route POST /spark/send-transfer
   * @param {string} receiverSparkAddress - The Spark address of the receiver
   * @param {number} amountSats - The amount to send in satoshis
   * @returns {Promise<{
   *   Promise<{
   *   data: {
   *     transfer: SparkProto.Transfer
   *   }
   * }>}
   */
  router.post(
    "/spark/send-transfer",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { receiverSparkAddress, amountSats } = req.body as {
          receiverSparkAddress: string;
          amountSats: number;
        };
        const transfer = await wallet!.transfer({
          receiverSparkAddress,
          amountSats,
        });
        const transferResponse = formatTransferResponse(transfer);
        res.json({
          data: { transfer: transferResponse },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Create lightning invoice
   * @route POST /lightning/create-invoice
   * @param {number} amountSats - The amount to create the invoice for in satoshis
   * @param {string} [memo] - The memo for the invoice
   * @param {number} [expirySeconds] - The expiry time for the invoice in seconds
   * @returns {Promise<{
   *   data: {
   *     invoice: string
   *   }
   * }>}
   */
  router.post(
    "/lightning/create-invoice",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { amountSats, memo, expirySeconds } = req.body as {
          amountSats: number;
          memo: string | undefined;
          expirySeconds: number | undefined;
        };
        const invoice = await wallet!.createLightningInvoice({
          amountSats,
          memo,
          expirySeconds,
        });
        res.json({
          data: { invoice },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

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
  router.post(
    "/lightning/pay-invoice",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { invoice } = req.body as { invoice: string };
        const payment = await wallet!.payLightningInvoice({ invoice });
        res.json({
          data: { payment },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Generate deposit address
   * @route GET /bitcoin/deposit-address
   * @returns {Promise<{
   *   data: {
   *     address: string
   *   }
   * }>}
   */
  router.get(
    "/bitcoin/deposit-address",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const address = await wallet!.getDepositAddress();
        res.json({
          data: { address },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Get L1 Address used for funding L1 token transactions like announce and withdraw.
   * @route GET /bitcoin/token-l1-address
   * @returns {Promise<{
   *   data: {
   *     sparkAddress: string
   *   }
   * }>}
   */
  router.get(
    "/bitcoin/token-l1-address",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const address = await wallet!.getTokenL1Address();
        res.json({
          data: { address },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Claim deposit
   * @route POST /bitcoin/claim-deposit
   * @param {string} txid - The transaction ID of the deposit
   * @returns {Promise<{
   *   data: {
   *     leaves: {
   *       id: string
   *       treeId: string
   *       value: number
   *       parentNodeId?: string
   *       nodeTx: string // hex string of Uint8Array
   *       refundTx: string // hex string of Uint8Array
   *       vout: number
   *       verifyingPublicKey: string // hex string of Uint8Array
   *       ownerIdentityPublicKey: string // hex string of Uint8Array
   *       signingKeyshare: {
   *         ownerIdentifiers: string[]
   *         threshold: number
   *       }
   *       status: string
   *       network: string // mapped from NETWORK_MAP
   *     }[]
   *   }
   * }>}
   */
  router.post(
    "/bitcoin/claim-deposit",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { txid } = req.body as {
          txid: string;
        };
        const leaves = await wallet!.claimDeposit(txid);
        res.json({
          data: { leaves },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Withdraw to Bitcoin address
   * @route POST /bitcoin/withdraw
   * @param {string} onchainAddress - The Bitcoin address to withdraw to
   * @param {string} [targetAmountSats] - The amount to withdraw in satoshis
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
  router.post("/bitcoin/withdraw", checkWalletInitialized, async (req, res) => {
    const wallet = getWallet();
    try {
      const { onchainAddress, targetAmountSats } = req.body as {
        onchainAddress: string;
        targetAmountSats: number | undefined;
      };
      const withdrawal = await wallet!.withdraw({
        onchainAddress,
        targetAmountSats,
      });
      res.json({
        data: { withdrawal },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });

  /**
   * Transfer tokens
   * @route POST /tokens/transfer
   * @param {string} tokenPublicKey - The public key of the token to transfer
   * @param {number} tokenAmount - The amount to transfer
   * @param {string} receiverSparkAddress - The Spark address of the receiver
   * @returns {Promise<{
   *   data: {
   *     transferTx: string
   *   }
   * }>}
   */
  router.post("/tokens/transfer", checkWalletInitialized, async (req, res) => {
    const wallet = getWallet();
    try {
      const { tokenPublicKey, tokenAmount, receiverSparkAddress } =
        req.body as {
          tokenPublicKey: string;
          tokenAmount: number;
          receiverSparkAddress: string;
        };
      const transferTx = await wallet!.transferTokens({
        tokenPublicKey,
        tokenAmount: BigInt(tokenAmount),
        receiverSparkAddress,
      });
      res.json({
        data: { transferTx },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });

  /**
   * Withdraw tokens
   * @route POST /tokens/withdraw
   * @param {string} tokenPublicKey - The public key of the token to withdraw
   * @param {string} [receiverPublicKey] - The public key of the receiver
   * @param {string[]} [leafIds] - The IDs of the leaves to withdraw
   * @returns {Promise<{
   *   data: {
   *     withdrawal: string
   *   }
   * }>}
   */
  router.post("/tokens/withdraw", checkWalletInitialized, async (req, res) => {
    const wallet = getWallet();
    try {
      const { tokenPublicKey, receiverPublicKey, leafIds } = req.body as {
        tokenPublicKey: string;
        receiverPublicKey: string | undefined;
        leafIds: string[] | undefined;
      };
      const withdrawalTx = await wallet!.withdrawTokens(
        tokenPublicKey,
        receiverPublicKey ?? undefined,
        leafIds ?? undefined
      );
      res.json({
        data: { withdrawalTx },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });
  return { router, getWallet, checkWalletInitialized };
};

export default createSparkRouter(SparkWallet, SPARK_MNEMONIC_PATH).router;
