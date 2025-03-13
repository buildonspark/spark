import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import { createSparkRouter } from "./sparkRoutes.js";

const ISSUER_MNEMONIC_PATH = ".issuer-mnemonic";
const PRIVATE_KEY =
  process.env.ISSUER_PRIVATE_KEY || new Uint8Array(32).fill(1);
const privateKeyBuffer = Buffer.from(PRIVATE_KEY, "hex");
const wallet = new IssuerSparkWallet("REGTEST", privateKeyBuffer);
const router = createSparkRouter(wallet, ISSUER_MNEMONIC_PATH);

// Get wallet balance
router.get("/token-balance", async (req, res) => {
  const balance = await wallet.getIssuerTokenBalance();
  res.json({ balance });
});

// Get token public key info
router.get("/token-public-key-info", async (req, res) => {
  const tokenPublicKeyInfo = await wallet.getTokenPublicKeyInfo();
  res.json({ tokenPublicKeyInfo });
});

// Mint tokens
router.post("/spark/mint-tokens", async (req, res) => {
  const { tokenAmount } = req.body;
  const amountToMint = BigInt(tokenAmount);
  const mintedTokens = await wallet.mintTokens(amountToMint);
  res.json({ mintedTokens });
});

// Burn tokens
router.post("/spark/burn-tokens", async (req, res) => {
  const { tokenAmount } = req.body;
  const amountToBurn = BigInt(tokenAmount);
  const burnedTokens = await wallet.burnTokens(amountToBurn);
  res.json({ burnedTokens });
});

// Freeze tokens
router.post("/spark/freeze-tokens", async (req, res) => {
  const { ownerPublicKey } = req.body;
  const frozenTokens = await wallet.freezeTokens(ownerPublicKey);
  res.json({ frozenTokens });
});

// Unfreeze tokens
router.post("/spark/unfreeze-tokens", async (req, res) => {
  const { ownerPublicKey } = req.body;
  const unfrozenTokens = await wallet.unfreezeTokens(ownerPublicKey);
  res.json({ unfrozenTokens });
});

// Announce token L1
router.post("/on-chain/announce-token", async (req, res) => {
  const {
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
    feeRateSatsPerVb,
  } = req.body;
  const tokenL1 = await wallet.announceTokenL1(
    tokenName,
    tokenTicker,
    decimals,
    maxSupply,
    isFreezable,
    feeRateSatsPerVb
  );
  res.json({ tokenL1 });
});

// Mint tokens L1
router.post("/on-chain/mint-tokens", async (req, res) => {
  const { tokenAmount } = req.body;
  const mintedTokensL1 = await wallet.mintTokensL1(tokenAmount);
  res.json({ mintedTokensL1 });
});

// Transfer tokens L1
router.post("/on-chain/transfer-tokens", async (req, res) => {
  const { tokenAmount, receiverPublicKey } = req.body;
  const transferredTokensL1 = await wallet.transferTokensL1(
    tokenAmount,
    receiverPublicKey
  );
  res.json({ transferredTokensL1 });
});

export default router;
