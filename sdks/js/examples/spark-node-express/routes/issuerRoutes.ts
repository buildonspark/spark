import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { createSparkRouter } from "./sparkRoutes.js";
import { isError } from "@lightsparkdev/core";

const ISSUER_MNEMONIC_PATH = ".issuer-mnemonic";

const { router, getWallet } = await createSparkRouter(
  IssuerSparkWallet,
  ISSUER_MNEMONIC_PATH
);

/**
 * Gets the balance of the issuer's token
 * @route GET /issuer-wallet/token-balance
 * @returns {Promise<{
 *  data: {balance: string},
 * }>}
 *
 * @example
 * // Response
 * {
 *   "data": {
 *     "balance": "1000000000000000000",
 *   },
 * }
 */
router.get("/token-balance", async (req, res) => {
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
});

/**
 * Gets the public key info of the issuer's token
 * @route GET /issuer-wallet/token-public-key-info
 * @returns {Promise<{
 *   data: {
 *     tokenPublicKeyInfo: {
 *       announcement: TokenPubkeyAnnouncement,
 *       totalSupply: string,
 *     }
 *   },
 * }>}
 *
 * @example
 * // Response
 * {
 *   "data": {
 *     "tokenPublicKeyInfo": {
 *       "announcement": {
 *         "tokenPubkey": "0x1234567890abcdef",
 *         "name": "My Token",
 *         "symbol": "MTK",
 *         "decimal": 8,
 *       "maxSupply": "1000000000000000000",
 *       "isFreezable": true,
 *     },
 *     "totalSupply": "1000000000000000000",
 *   },
 * }
 */
router.get("/token-public-key-info", async (req, res) => {
  const wallet = getWallet() as IssuerSparkWallet;
  try {
    const tokenPublicKeyInfo = await wallet!.getTokenPublicKeyInfo();
    res.json({
      data: { tokenPublicKeyInfo },
    });
  } catch (error) {
    console.error(error);
    const errorMsg = isError(error) ? error.message : "Unknown error";
    res.status(500).json({ error: errorMsg });
  }
});

/**
 * Mint tokens
 * @route POST /issuer-wallet/spark/mint-tokens
 * @param {number} tokenAmount - The amount of tokens to mint
 * @returns {Promise<{
 *   data: {
 *     tokensMinted: string
 *   }
 * }>}
 *
 * @example
 * // Request
 * {
 *   "tokenAmount": "1000000000000000000",
 * }
 *
 * // Response
 * {
 *   "data": {
 *     "tokensMinted": "1000000000000000000",
 *   },
 * }
 */
router.post("/spark/mint-tokens", async (req, res) => {
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
});

/**
 * Burn tokens
 * @route POST /issuer-wallet/spark/burn-tokens
 * @param {number} tokenAmount - The amount of tokens to burn
 * @returns {Promise<{
 *   data: {
 *     tokensBurned: string
 *   }
 * }>}
 *
 * @example
 * // Request
 * {
 *   "tokenAmount": "1000000000000000000",
 * }
 *
 * // Response
 * {
 *   "data": {
 *     "tokensBurned": "1000000000000000000",
 *   },
 * }
 */
router.post("/spark/burn-tokens", async (req, res) => {
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
});

/**
 * Freeze tokens
 * @route POST /issuer-wallet/spark/freeze-tokens
 * @param {string} ownerPublicKey - The public key of the owner
 * @returns {Promise<{
 *   data: {
 *     impactedLeafIds: string[],
 *     impactedTokenAmount: string
 *   }
 * }>}
 *
 * @example
 * // Request
 * {
 *   "ownerPublicKey": "0x1234567890abcdef",
 * }
 *
 * // Response
 * {
 *   "data": {
 *     "impactedLeafIds": ["1", "2", "3"],
 *     "impactedTokenAmount": "1000000000000000000",
 *   },
 * }
 */

router.post("/spark/freeze-tokens", async (req, res) => {
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
});

/**
 * Unfreeze tokens
 * @route POST /issuer-wallet/spark/unfreeze-tokens
 * @param {string} ownerPublicKey - The public key of the owner
 * @returns {Promise<{
 *   data: {
 *     impactedLeafIds: string[],
 *     impactedTokenAmount: string
 *   }
 * }>}
 *
 * @example
 * // Request
 * {
 *   "ownerPublicKey": "0x1234567890abcdef",
 * }
 *
 * // Response
 * {
 *   "data": {
 *     "impactedLeafIds": ["uuid1", "uuid2", "uuid3"],
 *     "impactedTokenAmount": "1000000000000000000",
 *   },
 * }
 */
router.post("/spark/unfreeze-tokens", async (req, res) => {
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
});

/**
 * Announce token L1
 * @route POST /issuer-wallet/on-chain/announce-token
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
 *
 * // Response
 * {
 *   "data": {
 *     "announcementTx": "0x1234567890abcdef",
 *   },
 * }
 */
router.post("/on-chain/announce-token", async (req, res) => {
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
});

/**
 * Mint tokens L1
 * @route POST /issuer-wallet/on-chain/mint-tokens
 * @param {number} tokenAmount - The amount of tokens to mint
 * @returns {Promise<{
 *   data: {
 *     tokensMinted: string
 *   }
 * }>}
 *
 * @example
 * // Request
 * {
 *   "tokenAmount": "1000000000000000000",
 * }
 *
 * // Response
 * {
 *   "data": {
 *     "tokensMinted": "1000000000000000000",
 *   },
 * }
 */
router.post("/on-chain/mint-tokens", async (req, res) => {
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
});

/**
 * Transfer tokens L1
 * @route POST /issuer-wallet/on-chain/transfer-tokens
 * @param {number} tokenAmount - The amount of tokens to transfer
 * @param {string} receiverPublicKey - The public key of the receiver
 * @returns {Promise<{
 *   data: {
 *     tokensTransferred: string
 *   }
 * }>}
 *
 * @example
 * // Request
 * {
 *   "tokenAmount": "1000000000000000000",
 *   "receiverPublicKey": "0x1234567890abcdef",
 * }
 *
 * // Response
 * {
 *   "data": {
 *     "tokensTransferred": "1000000000000000000",
 *   },
 * }
 */
router.post("/on-chain/transfer-tokens", async (req, res) => {
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
});

export default router;
