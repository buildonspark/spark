import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { SparkWallet, type TokenInfo } from "@buildonspark/spark-sdk";
import { Transfer } from "@buildonspark/spark-sdk/proto/spark";
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import {
  ExitSpeed,
  WalletTransfer,
  type CoopExitRequest,
  type LightningReceiveRequest,
  type LightningSendRequest,
} from "@buildonspark/spark-sdk/types";
import { getLatestDepositTxId } from "@buildonspark/spark-sdk/utils";
import { isError } from "@lightsparkdev/core";
import {
  Router,
  type NextFunction,
  type Request,
  type Response,
} from "express";
import fs from "fs";
import { BITCOIN_NETWORK } from "../src/index.js";
import {
  formatTransferResponse,
  loadMnemonic,
  saveMnemonic,
} from "../utils/utils.js";

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

  const parseConfig = (): ConfigOptions => {
    const configFile = process.env.CONFIG_FILE;
    let config: ConfigOptions = {};
    if (configFile) {
      try {
        const data = fs.readFileSync(configFile, "utf8");
        config = JSON.parse(data);
        if (config.network !== BITCOIN_NETWORK) {
          console.error("Network mismatch in config file");
        }
      } catch (err) {
        console.error("Error reading config file:", err);
      }
    }
    return config;
  };

  const initWallet = async (mnemonicOrSeed: string) => {
    let res:
      | {
          mnemonic?: string | null;
          wallet: SparkWallet | IssuerSparkWallet;
        }
      | undefined = undefined;
    if (!walletInstance) {
      res = await walletClass.initialize({
        mnemonicOrSeed: mnemonicOrSeed,
        options: {
          ...parseConfig(),
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
   *     transfers: Transfer[]
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
        (transfer: WalletTransfer) => formatTransferResponse(transfer)
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
   * Signs a message with the identity key.
   * This method can be useful if you have your own auth model for other APIs.
   *
   * @route POST /wallet/sign-message
   * @param {string} message - The message to sign
   * @param {boolean} [compactEncoding] - Whether or not to use compact encoding
   *  - If true, the message will be returned in ECDSA compact format
   *  - If false, the message will be returned in DER format
   *  - If not provided, the message will be returned in DER format
   * @returns {Promise<{
   *   data: {
   *     signedMessage: string
   *   }
   * }>}
   */
  router.post(
    "/wallet/sign-message",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { message, compactEncoding = false } = req.body as {
          message: string;
          compactEncoding: boolean;
        };
        const signedMessage = await wallet!.signMessageWithIdentityKey(
          message,
          compactEncoding
        );
        res.json({
          data: { signedMessage },
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
   *     transfer: Transfer
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
          receiverSparkAddress: receiverSparkAddress,
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
   *     invoice: LightningReceiveRequest
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
        const invoice: LightningReceiveRequest | null =
          await wallet!.createLightningInvoice({
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
   * @param {number} [maxFeeSats] - The maximum fee to pay in satoshis
   * @returns {Promise<{
   *   data: {
   *     payment: LightningSendRequest
   *   }
   * }>}
   */
  router.post(
    "/lightning/pay-invoice",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { invoice, maxFeeSats } = req.body as {
          invoice: string;
          maxFeeSats: number;
        };
        const payment: LightningSendRequest | null =
          await wallet!.payLightningInvoice({ invoice, maxFeeSats });
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
   * Get lightning receive request by Lightspark ID.
   * @route GET /lightning/receive-request
   * @param {string} id - The ID of the lightning receive request
   * @returns {Promise<{
   *   data: {
   *     receiveRequest: LightningReceiveRequest
   *   }
   * }>}
   */
  router.get(
    "/lightning/receive-request",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { id } = req.query as { id: string };
        const receiveRequest: LightningReceiveRequest | null =
          await wallet!.getLightningReceiveRequest(id);
        res.json({
          data: { receiveRequest },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Get lightning send request by Lightspark ID.
   * @route GET /lightning/send-request
   * @param {string} id - The ID of the lightning send request
   * @returns {Promise<{
   *   data: {
   *     sendRequest: LightningSendRequest
   *   }
   * }>}
   */
  router.get(
    "/lightning/send-request",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { id } = req.query as { id: string };
        const sendRequest: LightningSendRequest | null =
          await wallet!.getLightningSendRequest(id);
        res.json({
          data: { sendRequest },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Get lightning send fee estimate
   * @route GET /lightning/send-fee-estimate
   * @param {string} invoice - The encoded invoice to get the fee estimate for
   * @returns {Promise<{
   *   data: {
   *     feeEstimate: {
   *       originalValue: number
   *       originalUnit: string
   *       preferredCurrencyUnit: string
   *       preferredCurrencyValueRounded: number
   *       preferredCurrencyValueApprox: number
   *     }
   *   }
   * }>}
   */
  router.get(
    "/lightning/send-fee-estimate",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { invoice } = req.query as { invoice: string };
        const feeEstimate = await wallet!.getLightningSendFeeEstimate({
          encodedInvoice: invoice,
        });
        res.json({
          data: { feeEstimate },
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
   * @returns {Promise<{
   * @route GET /on-chain/spark-deposit-address
   *   data: {
   *     address: string
   *   }
   * }>}
   */
  router.get(
    "/on-chain/spark-deposit-address",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const address = await wallet!.getSingleUseDepositAddress();
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
   * Returns previously generated deposit addresses associated with this Spark Wallet.
   * @route GET /on-chain/unused-deposit-addresses
   * @returns {Promise<{
   *   data: {
   *     addresses: string[]
   *   }
   * }>}
   */
  router.get(
    "/on-chain/unused-deposit-addresses",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const addresses = await wallet!.getUnusedDepositAddresses();
        res.json({
          data: { addresses },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Returns the latest transaction ID deposited to the given Bitcoin address.
   * This txid can be used to claim the deposit using /on-chain/claim-deposit.
   * @route GET /on-chain/latest-deposit-txid
   * @param {string} btcAddress - The Bitcoin address to get the latest deposit transaction ID for
   * @returns {Promise<{
   *   data: {
   *     txid: string
   *   }
   * }>}
   */
  router.get("/on-chain/latest-deposit-txid", async (req, res) => {
    const { btcAddress } = req.query as { btcAddress: string };
    try {
      const txid = await getLatestDepositTxId(btcAddress);
      res.json({
        data: { txid },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });

  /**
   * Claim deposit
   * @route POST /on-chain/claim-deposit
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
    "/on-chain/claim-deposit",
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
   * @route POST /on-chain/withdraw
   * @param {string} onchainAddress - The Bitcoin address to withdraw to
   * @param {string} [amountSats] - The amount to withdraw in satoshis
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
  router.post(
    "/on-chain/withdraw",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { onchainAddress, amountSats, exitSpeed } = req.body as {
          onchainAddress: string;
          amountSats: number | undefined;
          exitSpeed: string;
        };
        const withdrawal = await wallet!.withdraw({
          onchainAddress,
          amountSats,
          exitSpeed: exitSpeed as ExitSpeed,
        });
        res.json({
          data: { withdrawal },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Gets fee estimate for cooperative exit (on-chain withdrawal).
   * @route GET /on-chain/get-coop-exit-fee-estimate
   * @param {number} amountSats - The amount to withdraw in satoshis
   * @param {string} withdrawalAddress - The address to withdraw to
   * @returns {Promise<{
   *   data: {
   *     feeEstimate: {
   *       originalValue: number
   *       originalUnit: string
   *       preferredCurrencyUnit: string
   *       preferredCurrencyValueRounded: number
   *       preferredCurrencyValueApprox: number
   *     }
   *   }
   * }>}
   */
  router.get(
    "/on-chain/get-coop-exit-fee-estimate",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { amountSats, withdrawalAddress } = req.query as {
          amountSats: string;
          withdrawalAddress: string;
        };
        const feeEstimate = await wallet!.getWithdrawalFeeEstimate({
          amountSats: Number(amountSats),
          withdrawalAddress,
        });
        res.json({
          data: { feeEstimate },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Get coop exit request by Lightspark ID.
   * @route GET /on-chain/coop-exit-request
   * @param {string} id - The ID of the coop exit request
   * @returns {Promise<{
   *   data: {
   *     coopExitRequest: CoopExitRequest
   *   }
   * }>}
   */
  router.get(
    "/on-chain/coop-exit-request",
    checkWalletInitialized,
    async (req, res) => {
      const wallet = getWallet();
      try {
        const { id } = req.query as { id: string };
        const coopExitRequest: CoopExitRequest | null =
          await wallet!.getCoopExitRequest(id);
        res.json({
          data: { coopExitRequest },
        });
      } catch (error) {
        console.error(error);
        const errorMsg = isError(error) ? error.message : "Unknown error";
        res.status(500).json({ error: errorMsg });
      }
    }
  );

  /**
   * Returns the token information for all tokens held in this wallet.
   * @route GET /tokens/info
   * @returns {Promise<{
   *   data: {
   *     tokenInfo: TokenInfo[]
   *   }
   * }>}
   */
  router.get("/tokens/info", checkWalletInitialized, async (req, res) => {
    const wallet = getWallet();
    try {
      const tokenInfo = await wallet!.getTokenInfo();
      res.json({
        data: { tokenInfo },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  });

  /**
   * Transfer tokens
   * @route POST /tokens/spark/transfer
   * @param {string} tokenPublicKey - The public key of the token to transfer
   * @param {number} tokenAmount - The amount to transfer
   * @param {string} receiverSparkAddress - The Spark address of the receiver
   * @returns {Promise<{
   *   data: {
   *     transferTx: string
   *   }
   * }>}
   */
  router.post(
    "/tokens/spark/transfer",
    checkWalletInitialized,
    async (req, res) => {
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
          receiverSparkAddress: receiverSparkAddress,
        });
        res.json({
          data: { transferTx },
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
   * @route GET /tokens/on-chain/token-l1-address
   * @returns {Promise<{
   *   data: {
   *     sparkAddress: string
   *   }
   * }>}
   */
  router.get(
    "/tokens/on-chain/token-l1-address",
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
   * Withdraw tokens
   * @route POST /tokens/on-chain/withdraw
   * @param {string} tokenPublicKey - The public key of the token to withdraw
   * @param {number} tokenAmount - The amount to withdraw
   * @returns {Promise<{
   *   data: {
   *     withdrawal: string
   *   }
   * }>}
   */
  return { router, getWallet, checkWalletInitialized };
};

export default createSparkRouter(SparkWallet, SPARK_MNEMONIC_PATH).router;
