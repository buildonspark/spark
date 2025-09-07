import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";

import { isError } from "@lightsparkdev/core";
import { createSparkRouter } from "./sparkRoutes.js";

const ISSUER_MNEMONIC_PATH = ".issuer-mnemonic";

const { router, getWallet, checkWalletInitialized } = createSparkRouter(
  IssuerSparkWallet,
  ISSUER_MNEMONIC_PATH,
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
        data: { balance: balance },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  },
);

/**
 * Gets the public key info of the issuer's token
 * @route GET /issuer-wallet/tokens/token-metadata
 * @returns {Promise<{
 *   data: {
 *     tokenMetadata: {
 *       announcement: TokenPubkeyAnnouncement,
 *       totalSupply: string,
 *     }
 *   },
 * }>}
 */
router.get(
  "/tokens/token-metadata",
  checkWalletInitialized,
  async (req, res) => {
    const wallet = getWallet() as IssuerSparkWallet;
    try {
      const tokenMetadata = await wallet!.getIssuerTokenMetadata();
      console.log("response: ", tokenMetadata);
      res.json({
        data: { tokenMetadata },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  },
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
      const tokenTransactionHash = await wallet!.mintTokens(
        BigInt(tokenAmount),
      );
      res.json({
        data: { tokenTransactionHash },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  },
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
  },
);

/**
 * Freeze tokens
 * @route POST /issuer-wallet/tokens/spark/freeze-tokens
 * @param {string} sparkAddress - The spark address of the owner
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
      const { sparkAddress } = req.body as { sparkAddress: string };
      const frozenTokens = await wallet!.freezeTokens(sparkAddress);
      res.json({
        data: {
          impactedOutputIds: frozenTokens.impactedOutputIds,
          impactedTokenAmount: frozenTokens.impactedTokenAmount,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  },
);

/**
 * Unfreeze tokens
 * @route POST /issuer-wallet/tokens/spark/unfreeze-tokens
 * @param {string} sparkAddress - The spark address of the owner
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
      const { sparkAddress } = req.body as { sparkAddress: string };
      const thawedTokens = await wallet!.unfreezeTokens(sparkAddress);
      res.json({
        data: {
          impactedOutputIds: thawedTokens.impactedOutputIds,
          impactedTokenAmount: thawedTokens.impactedTokenAmount,
        },
      });
    } catch (error) {
      console.error(error);
      const errorMsg = isError(error) ? error.message : "Unknown error";
      res.status(500).json({ error: errorMsg });
    }
  },
);

export default router;
