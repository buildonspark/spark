import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { createSparkRouter } from "./sparkRoutes.js";

const ISSUER_MNEMONIC_PATH = ".issuer-mnemonic";
const PRIVATE_KEY =
  process.env.ISSUER_PRIVATE_KEY || new Uint8Array(32).fill(1);
const privateKeyBuffer = Buffer.from(PRIVATE_KEY, "hex");
const wallet = new IssuerSparkWallet("REGTEST", privateKeyBuffer);
const router = createSparkRouter(wallet, ISSUER_MNEMONIC_PATH);

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
  try {
    const balance = await wallet.getIssuerTokenBalance();
    res.json({
      data: { balance },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
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
  try {
    const tokenPublicKeyInfo = await wallet.getTokenPublicKeyInfo();
    res.json({
      data: { tokenPublicKeyInfo },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
  }
});

/**
 * Mint tokens
 * @route POST /issuer-wallet/spark/mint-tokens
 * @param {string} tokenAmount - The amount of tokens to mint
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
  try {
    const { tokenAmount } = req.body;
    const amountToMint = BigInt(tokenAmount);
    const tokensMinted = await wallet.mintTokens(amountToMint);
    res.json({
      data: { tokensMinted },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
  }
});

/**
 * Burn tokens
 * @route POST /issuer-wallet/spark/burn-tokens
 * @param {string} tokenAmount - The amount of tokens to burn
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
  try {
    const { tokenAmount } = req.body;
    const amountToBurn = BigInt(tokenAmount);
    const tokensBurned = await wallet.burnTokens(amountToBurn);
    res.json({
      data: { tokensBurned },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
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
  try {
    const { ownerPublicKey } = req.body;
    const frozenTokens = await wallet.freezeTokens(ownerPublicKey);
    res.json({
      data: {
        impactedLeafIds: frozenTokens.impactedLeafIds,
        impactedTokenAmount: frozenTokens.impactedTokenAmount,
      },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
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
  const { ownerPublicKey } = req.body;
  const thawedTokens = await wallet.unfreezeTokens(ownerPublicKey);
  res.json({
    data: {
      impactedLeafIds: thawedTokens.impactedLeafIds,
      impactedTokenAmount: thawedTokens.impactedTokenAmount,
    },
  });
});

/**
 * Announce token L1
 * @route POST /issuer-wallet/on-chain/announce-token
 * @param {string} tokenName - The name of the token
 * @param {string} tokenTicker - The ticker of the token
 * @param {number} decimals - The number of decimals of the token
 * @param {string} maxSupply - The maximum supply of the token
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
 *   "maxSupply": "1000000000000000000",
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
  const {
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
    feeRateSatsPerVb,
  } = req.body;
  try {
    const announcementTx = await wallet.announceTokenL1(
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      isFreezable,
      feeRateSatsPerVb
    );
    res.json({
      data: { announcementTx },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
  }
});

/**
 * Mint tokens L1
 * @route POST /issuer-wallet/on-chain/mint-tokens
 * @param {string} tokenAmount - The amount of tokens to mint
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
  try {
    const { tokenAmount } = req.body;
    const tokensMinted = await wallet.mintTokensL1(tokenAmount);
    res.json({
      data: { tokensMinted },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
  }
});

/**
 * Transfer tokens L1
 * @route POST /issuer-wallet/on-chain/transfer-tokens
 * @param {string} tokenAmount - The amount of tokens to transfer
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
  try {
    const { tokenAmount, receiverPublicKey } = req.body;
    const tokensTransferred = await wallet.transferTokensL1(
      tokenAmount,
      receiverPublicKey
    );
    res.json({
      data: { tokensTransferred },
    });
  } catch (error) {
    res.status(500).json({
      error: error.message,
    });
  }
});

export default router;
