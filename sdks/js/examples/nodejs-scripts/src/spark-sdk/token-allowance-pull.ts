/**
 * Token allowance pull demo (Spark Pull, token engine).
 *
 * Full lifecycle against a running local Spark stack (run-everything.sh):
 *   1. An owner (issuer) wallet creates a token, mints supply, and grants a
 *      spender wallet a bounded allowance (per-tx cap, total limit).
 *   2. The spender queries the allowances naming it as spender.
 *   3. The spender pulls an amount from the owner's outputs to a third
 *      merchant-settlement wallet, with the owner's change appended
 *      automatically. Balances are printed before and after.
 *   4. The owner revokes the allowance.
 *   5. The spender's next pull fails with the typed
 *      a REVOKED allowance failure.
 *
 * Environment (all optional):
 *   BITCOIN_NETWORK / NETWORK / SPARK_NETWORK  network, default LOCAL
 *   OWNER_MNEMONIC / SPENDER_MNEMONIC / MERCHANT_MNEMONIC
 *       wallet mnemonics; fresh wallets are generated (and their mnemonics
 *       printed) when unset
 *   MINT_AMOUNT      tokens minted to the owner        (default 100000)
 *   PULL_AMOUNT      tokens pulled to the merchant     (default 1000)
 *   PER_TX_CAP       allowance per-transaction cap     (default 5000)
 *   TOTAL_LIMIT      allowance lifetime limit          (default 20000)
 *   EXPIRY_MINUTES   allowance expiry from now         (default 60)
 *   CONFIG_FILE      JSON ConfigOptions override (see wallet-config.ts)
 */
import { IssuerSparkWallet } from "@buildonspark/issuer-sdk";
import {
  SparkWallet,
  decodeBech32mTokenIdentifier,
  type Bech32mTokenIdentifier,
  type NetworkType,
  tokenAllowanceFailureOf,
} from "@buildonspark/spark-sdk";
import {
  getExampleSparkNetwork,
  getExampleWalletOptions,
} from "./wallet-config.js";

function bytesToHex(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString("hex");
}

function envBigInt(name: string, defaultValue: bigint): bigint {
  const raw = process.env[name];
  return raw === undefined ? defaultValue : BigInt(raw);
}

function envInt(name: string, defaultValue: number): number {
  const raw = process.env[name];
  return raw === undefined ? defaultValue : Number.parseInt(raw, 10);
}

const network: NetworkType = getExampleSparkNetwork(
  {
    ...process.env,
    NETWORK: process.env["NETWORK"] ?? process.env["BITCOIN_NETWORK"],
  },
  "LOCAL",
);
const envWithNetwork = { ...process.env, NETWORK: network };
const options = getExampleWalletOptions(envWithNetwork, network);

const mintAmount = envBigInt("MINT_AMOUNT", 100_000n);
const pullAmount = envBigInt("PULL_AMOUNT", 1_000n);
const perTransactionCap = envBigInt("PER_TX_CAP", 5_000n);
const totalLimit = envBigInt("TOTAL_LIMIT", 20_000n);
const expiryMinutes = envInt("EXPIRY_MINUTES", 60);

console.log(`Token allowance pull demo on ${network}`);
console.log(
  `mint=${mintAmount} pull=${pullAmount} perTxCap=${perTransactionCap}` +
    ` totalLimit=${totalLimit}`,
);

async function reportWallet<T extends SparkWallet>(
  role: string,
  mnemonicEnv: string,
  init: { wallet: T; mnemonic?: string },
): Promise<{ wallet: T; identityPublicKey: string }> {
  if (!process.env[mnemonicEnv] && init.mnemonic) {
    console.log(`${role}: generated new wallet (set ${mnemonicEnv} to reuse)`);
    console.log(`${role} mnemonic: ${init.mnemonic}`);
  }
  const identityPublicKey = await init.wallet.getIdentityPublicKey();
  console.log(`${role} identity public key: ${identityPublicKey}`);
  return { wallet: init.wallet, identityPublicKey };
}

const { wallet: ownerWallet, identityPublicKey: ownerPublicKeyHex } =
  await reportWallet(
    "owner (issuer)",
    "OWNER_MNEMONIC",
    await IssuerSparkWallet.initialize({
      mnemonicOrSeed: process.env["OWNER_MNEMONIC"],
      options,
    }),
  );
const { wallet: spenderWallet, identityPublicKey: spenderPublicKeyHex } =
  await reportWallet(
    "spender (merchant)",
    "SPENDER_MNEMONIC",
    await SparkWallet.initialize({
      mnemonicOrSeed: process.env["SPENDER_MNEMONIC"],
      options,
    }),
  );
const { wallet: merchantWallet, identityPublicKey: merchantPublicKeyHex } =
  await reportWallet(
    "merchant settlement",
    "MERCHANT_MNEMONIC",
    await SparkWallet.initialize({
      mnemonicOrSeed: process.env["MERCHANT_MNEMONIC"],
      options,
    }),
  );

// 1. Owner creates (or reuses) its token and mints supply to itself.
const issuerWallet = ownerWallet;
let bech32mTokenIdentifier: Bech32mTokenIdentifier;
const existingTokens = await issuerWallet.getIssuerTokensMetadata();
if (existingTokens.length > 0) {
  bech32mTokenIdentifier = existingTokens[0].bech32mTokenIdentifier;
  console.log(`Reusing existing token ${bech32mTokenIdentifier}`);
} else {
  const creation = await issuerWallet.createToken({
    tokenName: "PullDemoToken",
    tokenTicker: "PULL",
    decimals: 0,
    isFreezable: false,
    maxSupply: 0n,
    returnIdentifierForCreate: true,
  });
  bech32mTokenIdentifier = creation.tokenIdentifier;
  console.log(`Created token ${bech32mTokenIdentifier}`);
}
const { tokenIdentifier: tokenIdentifierBytes } = decodeBech32mTokenIdentifier(
  bech32mTokenIdentifier,
  network,
);
// The allowance API takes hex; decode returns the raw bytes.
const tokenIdentifier = bytesToHex(tokenIdentifierBytes);

const mintTxId = await issuerWallet.mintTokens({
  tokenAmount: mintAmount,
  tokenIdentifier: bech32mTokenIdentifier,
});
console.log(`Minted ${mintAmount} to owner (tx ${mintTxId})`);

// The SO allows at most one active grant per (owner, spender, token), so revoke
// any grant left active by an interrupted earlier run before granting again.
const spenderHex = spenderPublicKeyHex;
const preexisting = await ownerWallet.queryTokenAllowances({
  ownerPublicKey: ownerPublicKeyHex,
  tokenIdentifier,
});
for (const info of preexisting) {
  if (
    info.allowancePayload &&
    bytesToHex(info.allowancePayload.spenderPublicKey) === spenderHex
  ) {
    await ownerWallet.revokeTokenAllowance(
      bytesToHex(info.allowancePayload.allowanceId),
    );
    console.log(
      `Revoked leftover active grant for ${spenderHex.slice(0, 12)}…`,
    );
  }
}

async function tokenBalanceOf(
  wallet: InstanceType<typeof SparkWallet>,
): Promise<bigint> {
  const { tokenBalances } = await wallet.getBalance();
  return tokenBalances.get(bech32mTokenIdentifier)?.ownedBalance ?? 0n;
}

async function printBalances(label: string) {
  console.log(`--- balances ${label} ---`);
  console.log(`owner:    ${await tokenBalanceOf(ownerWallet)}`);
  console.log(`spender:  ${await tokenBalanceOf(spenderWallet)}`);
  console.log(`merchant: ${await tokenBalanceOf(merchantWallet)}`);
}

// 2. Owner grants the spender a bounded allowance against its own balance.
const { allowanceId } = await ownerWallet.createTokenAllowance({
  spenderPublicKey: spenderPublicKeyHex,
  tokenIdentifier,
  perTransaction: { amount: perTransactionCap },
  total: { amount: totalLimit },
  expiryTime: new Date(Date.now() + expiryMinutes * 60_000),
});
console.log(`Owner created allowance ${allowanceId}`);

// 3. Spender discovers the allowances naming it as spender.
const allowances = await spenderWallet.queryTokenAllowances({
  spenderPublicKey: spenderPublicKeyHex,
});
console.log(`Spender sees ${allowances.length} active allowance(s):`);
for (const info of allowances) {
  const payload = info.allowancePayload;
  if (!payload) {
    continue;
  }
  console.log(
    `  id=${bytesToHex(payload.allowanceId)}` +
      ` owner=${bytesToHex(payload.ownerPublicKey)}` +
      ` spent=${BigInt(`0x${bytesToHex(info.spentAmount) || "0"}`)}`,
  );
}

await printBalances("before pull");

// 4. Spender pulls tokens from the owner to the merchant settlement wallet.
// The owner's change is appended automatically.
const preparedPull = await spenderWallet.startAllowancePull({
  allowanceId,
  ownerPublicKey: ownerPublicKeyHex,
  tokenIdentifier,
  outputs: [
    { receiverPublicKey: merchantPublicKeyHex, tokenAmount: pullAmount },
  ],
});
const { txId } = await spenderWallet.commitAllowancePull(preparedPull);
console.log(`Pull of ${pullAmount} settled (tx ${txId})`);

await printBalances("after pull");

// 5. Owner revokes; the spender's next pull must fail with the typed error.
await ownerWallet.revokeTokenAllowance(allowanceId);
console.log(`Owner revoked allowance ${allowanceId}`);

let failures = 0;
try {
  const secondPull = await spenderWallet.startAllowancePull({
    allowanceId,
    ownerPublicKey: ownerPublicKeyHex,
    tokenIdentifier,
    outputs: [
      { receiverPublicKey: merchantPublicKeyHex, tokenAmount: pullAmount },
    ],
  });
  await spenderWallet.commitAllowancePull(secondPull);
  console.error("ERROR: pull after revocation unexpectedly succeeded");
  failures++;
} catch (error) {
  const failure = tokenAllowanceFailureOf(error);
  if (failure === "REVOKED") {
    console.log(`Pull after revocation failed as expected: ${failure}`);
  } else {
    throw error;
  }
}

await printBalances("final");

await Promise.all([
  ownerWallet.cleanupConnections(),
  spenderWallet.cleanupConnections(),
  merchantWallet.cleanupConnections(),
]);

if (failures > 0) {
  console.error(`\nAllowance pull demo FAILED (${failures} check(s))`);
  process.exit(1);
}
console.log("Demo complete");
