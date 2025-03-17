import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

import { Lrc20Protos } from "@buildonspark/lrc20-sdk";
import { createSparkRouter } from "./sparkRoutes.js";
import { isError } from "@lightsparkdev/core";
import { hexToBytes } from "@noble/curves/abstract/utils";

const ISSUER_MNEMONIC_PATH = ".issuer-mnemonic";

const { router, getWallet, checkWalletInitialized } = createSparkRouter(
  IssuerSparkWallet,
  ISSUER_MNEMONIC_PATH
);

/**
 * Gets the balance of the issuer's token
 * @route GET /issuer-wallet/tokens/token-balance
 * @returns {Promise<{
 *  data: {balance: string},
 * }>}
 */
router.get(
  "/tokens/token-balance",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const balance = await wallet!.getIssuerTokenBalance();
      res.json({
        data: { balance: balance.balance },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Gets the public key info of the issuer's token
 * @route GET /issuer-wallet/tokens/token-public-key-info
 * @returns {Promise<{
 *   data: {
 *     tokenPublicKeyInfo: {
 *       announcement: TokenPubkeyAnnouncement,
 *       totalSupply: string,
 *     }
 *   },
 * }>}
 */
router.get(
  "/tokens/token-public-key-info",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const tokenPublicKeyInfo = await wallet!.getTokenPublicKeyInfo();
      console.log("response: ", tokenPublicKeyInfo);
      res.json({
        data: { tokenPublicKeyInfo },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Gets the activity of the issuer's token
 * @route GET /issuer-wallet/tokens/token-activity
 * @param {number} [pageSize = 20] - The number of transactions to return
 * @param {string} [lastTransactionHash] - The hash of the last transaction as a hex string
 * @param {string} [layer] - The layer of the last transaction "L1" or "SPARK"
 * @param {Lrc20Protos.ListAllTokenTransactionsCursor} cursor - The cursor to start from
 * @returns {Promise<{
 *   data: {
 *     transactions: Transaction[]
 *     nextCursor: ListAllTokenTransactionsCursor
 *   }
 * }>}
 */
router.get(
  "/tokens/token-activity",
  checkWalletInitialized,
  async (req, res) => {
    const {
      pageSize = 20,
      lastTransactionHash,
      layer,
    } = req.query as {
      pageSize: number | undefined;
      lastTransactionHash: string | undefined;
      layer: string | undefined;
    };
    const wallet = getWallet() as IssuerSparkWallet;
    const cursor =
      lastTransactionHash && layer
        ? {
            lastTransactionHash: hexToBytes(lastTransactionHash),
            layer: Lrc20Protos.Layer[layer as keyof typeof Lrc20Protos.Layer],
          }
        : undefined;
    try {
      const tokenActivity = await wallet!.getTokenActivity(
        Number(pageSize),
        cursor ? cursor : undefined
      );
      res.json({
        data: {
          transactions: tokenActivity.transactions,
          nextCursor: tokenActivity.nextCursor,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Gets the activity of the issuer's token
 * @route GET /issuer-wallet/tokens/issuer-token-activity
 * @param {number} [pageSize = 20] - The number of transactions to return
 * @param {string} [lastTransactionHash] - The hash of the last transaction as a hex string
 * @param {string} [layer] - The layer of the last transaction "L1" or "SPARK"
 * @returns {Promise<{
 *   data: {
 *     issuerTokenActivity: Transaction[]
 *     nextCursor: ListAllTokenTransactionsCursor
 *   }
 * }>}
 */
router.get(
  "/tokens/issuer-token-activity",
  checkWalletInitialized,
  async (req, res) => {
    const {
      pageSize = 20,
      lastTransactionHash,
      layer,
    } = req.query as {
      pageSize: number | undefined;
      lastTransactionHash: string | undefined;
      layer: string | undefined;
    };
    const wallet = getWallet() as IssuerSparkWallet;
    const cursor =
      lastTransactionHash && layer
        ? {
            lastTransactionHash: hexToBytes(lastTransactionHash),
            layer: Lrc20Protos.Layer[layer as keyof typeof Lrc20Protos.Layer],
          }
        : undefined;
    try {
      const issuerTokenActivity = await wallet!.getIssuerTokenActivity(
        pageSize ? Number(pageSize) : undefined,
        cursor ? cursor : undefined
      );
      res.json({
        data: {
          issuerTokenActivity: issuerTokenActivity.transactions,
          nextCursor: issuerTokenActivity.nextCursor,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Mint tokens
 * @route POST /issuer-wallet/tokens/spark/mint-tokens
 * @param {number} tokenAmount - The amount of tokens to mint
 * @returns {Promise<{
 *   data: {
 *     tokensMinted: string
 *   }
 * }>}
 */
router.post(
  "/tokens/spark/mint-tokens",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const { tokenAmount } = req.body as { tokenAmount: number };
      const tokensMinted = await wallet!.mintTokens(BigInt(tokenAmount));
      res.json({
        data: { tokensMinted },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Burn tokens
 * @route POST /issuer-wallet/tokens/spark/burn-tokens
 * @param {number} tokenAmount - The amount of tokens to burn
 * @returns {Promise<{
 *   data: {
 *     tokensBurned: string
 *   }
 * }>}
 */
router.post(
  "/tokens/spark/burn-tokens",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const { tokenAmount } = req.body as { tokenAmount: number };
      const tokensBurned = await wallet!.burnTokens(BigInt(tokenAmount));
      res.json({
        data: { tokensBurned },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Freeze tokens
 * @route POST /issuer-wallet/tokens/spark/freeze-tokens
 * @param {string} ownerPublicKey - The public key of the owner
 * @returns {Promise<{
 *   data: {
 *     impactedLeafIds: string[],
 *     impactedTokenAmount: string
 *   }
 * }>}
 */

router.post(
  "/tokens/spark/freeze-tokens",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const { ownerPublicKey } = req.body as { ownerPublicKey: string };
      const frozenTokens = await wallet!.freezeTokens(ownerPublicKey);
      res.json({
        data: {
          impactedLeafIds: frozenTokens.impactedLeafIds,
          impactedTokenAmount: frozenTokens.impactedTokenAmount,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Unfreeze tokens
 * @route POST /issuer-wallet/tokens/spark/unfreeze-tokens
 * @param {string} ownerPublicKey - The public key of the owner
 * @returns {Promise<{
 *   data: {
 *     impactedLeafIds: string[],
 *     impactedTokenAmount: string
 *   }
 * }>}
 */
router.post(
  "/tokens/spark/unfreeze-tokens",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const { ownerPublicKey } = req.body as { ownerPublicKey: string };
      const thawedTokens = await wallet!.unfreezeTokens(ownerPublicKey);
      res.json({
        data: {
          impactedLeafIds: thawedTokens.impactedLeafIds,
          impactedTokenAmount: thawedTokens.impactedTokenAmount,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Announce token L1
 * @route POST /issuer-wallet/tokens/on-chain/announce-token
 * @param {string} tokenName - The name of the token
 * @param {string} tokenTicker - The ticker of the token
 * @param {number} decimals - The number of decimals of the token
 * @param {number} maxSupply - The maximum supply of the token
 * @param {boolean} isFreezable - Whether the token is freezable
 * @param {number} [feeRateSatsPerVb] - The fee rate in sats per vbyte
 * @returns {Promise<{announcementTx: string}>}
 *
 * @example
 * // Request
 * {
 *   "tokenName": "My Token",
 *   "tokenTicker": "MTK",
 *   "decimals": 8,
 *   "maxSupply": 1000000000000000000,
 *   "isFreezable": true,
 *   "feeRateSatsPerVb": 2.0,
 * }
 */
router.post(
  "/tokens/on-chain/announce-token",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const {
        tokenName,
        tokenTicker,
        decimals,
        maxSupply,
        isFreezable,
        feeRateSatsPerVb,
      } = req.body as {
        tokenName: string;
        tokenTicker: string;
        decimals: number;
        maxSupply: number;
        isFreezable: boolean;
        feeRateSatsPerVb: number | undefined;
      };
      const announcementTx = await wallet!.announceTokenL1({
        tokenName,
        tokenTicker,
        decimals: Number(decimals),
        maxSupply: BigInt(maxSupply),
        isFreezable,
        feeRateSatsPerVb,
      });
      res.json({
        data: { announcementTx },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Mint tokens L1
 * @route POST /issuer-wallet/tokens/on-chain/mint-tokens
 * @param {number} tokenAmount - The amount of tokens to mint
 * @returns {Promise<{
 *   data: {
 *     tokensMinted: string
 *   }
 * }>}
 */
router.post(
  "/tokens/on-chain/mint-tokens",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const { tokenAmount } = req.body as { tokenAmount: number };
      const tokensMinted = await wallet!.mintTokensL1(BigInt(tokenAmount));
      res.json({
        data: { tokensMinted },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

/**
 * Transfer tokens L1
 * @route POST /issuer-wallet/tokens/on-chain/transfer-tokens
 * @param {number} tokenAmount - The amount of tokens to transfer
 * @param {string} receiverPublicKey - The public key of the receiver
 * @returns {Promise<{
 *   data: {
 *     tokensTransferred: string
 *   }
 * }>}
 */
router.post(
  "/tokens/on-chain/transfer-tokens",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const { tokenAmount, receiverPublicKey } = req.body as {
        tokenAmount: number;
        receiverPublicKey: string;
      };
      const tokensTransferred = await wallet!.transferTokensL1(
        BigInt(tokenAmount),
        receiverPublicKey
      );
      res.json({
        data: { tokensTransferred },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  }
);

export default router;
