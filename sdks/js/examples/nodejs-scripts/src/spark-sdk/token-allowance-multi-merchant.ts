/**
 * Multi-merchant token allowance demo (Spark Pull, token engine).
 *
 * Proves that TWO independent merchants can hold delegated access to the SAME
 * owner balance simultaneously, each metered by its own allowance, and that
 * revoking one does not affect the other. Runs live against a local Spark stack
 * (run-everything.sh) and exits 0 only when every assertion holds:
 *
 *   1. The owner (issuer) mints a supply and grants Merchant A AND Merchant B
 *      bounded allowances against the same token pool.
 *   2. Merchant A pulls to the settlement wallet            -> succeeds.
 *   3. Merchant B pulls to the settlement wallet            -> succeeds.
 *      (Both draw from the owner's single balance; their spent meters advance
 *      independently.)
 *   4. The owner revokes Merchant A only.
 *   5. Merchant A's next pull                               -> a REVOKED allowance failure.
 *   6. Merchant B pulls again                               -> still succeeds.
 *
 * Balances and per-allowance spent amounts are printed after each step.
 *
 * Environment (all optional):
 *   NETWORK / BITCOIN_NETWORK / SPARK_NETWORK   network, default LOCAL
 *   OWNER_MNEMONIC        issuer / owner wallet
 *   SPENDER_MNEMONIC      Merchant A ("Bitrefill")
 *   MERCHANT_B_MNEMONIC   Merchant B ("CoinCorner"); generated + printed if unset
 *   MERCHANT_MNEMONIC     settlement destination wallet
 *   MINT_AMOUNT           tokens minted to the owner       (default 100000)
 *   PULL_AMOUNT           tokens pulled per pull           (default 1000)
 *   Run with NUM_SPARK_OPERATORS matching the local operator count and
 *   SPARK_DANGEROUSLY_DISABLE_TLS_VERIFICATION=1 for the self-signed local certs.
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

function bytesToBigInt(bytes: Uint8Array): bigint {
  return bytes.length === 0 ? 0n : BigInt(`0x${bytesToHex(bytes)}`);
}

function envBigInt(name: string, defaultValue: bigint): bigint {
  const raw = process.env[name];
  return raw === undefined ? defaultValue : BigInt(raw);
}

const network: NetworkType = getExampleSparkNetwork(
  {
    ...process.env,
    NETWORK: process.env["NETWORK"] ?? process.env["BITCOIN_NETWORK"],
  },
  "LOCAL",
);
const options = getExampleWalletOptions(
  { ...process.env, NETWORK: network },
  network,
);

const mintAmount = envBigInt("MINT_AMOUNT", 100_000n);
const pullAmount = envBigInt("PULL_AMOUNT", 1_000n);

console.log(`Token allowance MULTI-MERCHANT demo on ${network}`);

const { wallet: ownerWallet, mnemonic: ownerMnemonic } =
  await IssuerSparkWallet.initialize({
    mnemonicOrSeed: process.env["OWNER_MNEMONIC"],
    options,
  });
if (!process.env["OWNER_MNEMONIC"] && ownerMnemonic) {
  console.log(`owner (issuer) generated mnemonic: ${ownerMnemonic}`);
}

const { wallet: merchantA, mnemonic: merchantAMnemonic } =
  await SparkWallet.initialize({
    mnemonicOrSeed: process.env["SPENDER_MNEMONIC"],
    options,
  });
if (!process.env["SPENDER_MNEMONIC"] && merchantAMnemonic) {
  console.log(`Merchant A generated mnemonic: ${merchantAMnemonic}`);
}

const { wallet: merchantB, mnemonic: merchantBMnemonic } =
  await SparkWallet.initialize({
    mnemonicOrSeed: process.env["MERCHANT_B_MNEMONIC"],
    options,
  });
if (!process.env["MERCHANT_B_MNEMONIC"] && merchantBMnemonic) {
  console.log(`Merchant B generated mnemonic: ${merchantBMnemonic}`);
}

const { wallet: settlementWallet, mnemonic: settlementMnemonic } =
  await SparkWallet.initialize({
    mnemonicOrSeed: process.env["MERCHANT_MNEMONIC"],
    options,
  });
if (!process.env["MERCHANT_MNEMONIC"] && settlementMnemonic) {
  console.log(`settlement generated mnemonic: ${settlementMnemonic}`);
}

const ownerPublicKey = await ownerWallet.getIdentityPublicKey();
const merchantAKey = await merchantA.getIdentityPublicKey();
const merchantBKey = await merchantB.getIdentityPublicKey();
const settlementKey = await settlementWallet.getIdentityPublicKey();

// Token + supply (reuse the owner's existing issuer token when present).
let bech32mTokenIdentifier: Bech32mTokenIdentifier;
const existingTokens = await ownerWallet.getIssuerTokensMetadata();
if (existingTokens.length > 0) {
  bech32mTokenIdentifier = existingTokens[0].bech32mTokenIdentifier;
} else {
  const creation = await ownerWallet.createToken({
    tokenName: "PullMultiDemoToken",
    tokenTicker: "PULLM",
    decimals: 0,
    isFreezable: false,
    maxSupply: 0n,
    returnIdentifierForCreate: true,
  });
  bech32mTokenIdentifier = creation.tokenIdentifier;
}
const { tokenIdentifier: tokenIdentifierBytes } = decodeBech32mTokenIdentifier(
  bech32mTokenIdentifier,
  network,
);
// The allowance API takes hex; decode returns the raw bytes.
const tokenIdentifier = bytesToHex(tokenIdentifierBytes);
await ownerWallet.mintTokens({
  tokenAmount: mintAmount,
  tokenIdentifier: bech32mTokenIdentifier,
});
console.log(`Token ${bech32mTokenIdentifier}, minted ${mintAmount} to owner`);

async function tokenBalanceOf(
  wallet: InstanceType<typeof SparkWallet>,
): Promise<bigint> {
  const { tokenBalances } = await wallet.getBalance();
  return tokenBalances.get(bech32mTokenIdentifier)?.ownedBalance ?? 0n;
}

async function spentOf(allowanceId: string): Promise<bigint> {
  const allowances = await ownerWallet.queryTokenAllowances({
    ownerPublicKey,
    tokenIdentifier,
    includeInactive: true,
  });
  const idHex = allowanceId;
  const info = allowances.find(
    (a) =>
      a.allowancePayload &&
      bytesToHex(a.allowancePayload.allowanceId) === idHex,
  );
  return info ? bytesToBigInt(info.spentAmount) : 0n;
}

async function report(label: string, ids: Record<string, string>) {
  const [owner, settlement, a, b] = await Promise.all([
    tokenBalanceOf(ownerWallet),
    tokenBalanceOf(settlementWallet),
    tokenBalanceOf(merchantA),
    tokenBalanceOf(merchantB),
  ]);
  console.log(`\n--- ${label} ---`);
  console.log(
    `  balances: owner=${owner} settlement=${settlement} A=${a} B=${b}`,
  );
  for (const [name, id] of Object.entries(ids)) {
    console.log(`  spent[${name}]=${await spentOf(id)}`);
  }
}

let failures = 0;
function check(condition: boolean, message: string) {
  if (condition) {
    console.log(`  PASS: ${message}`);
  } else {
    console.error(`  FAIL: ${message}`);
    failures++;
  }
}

// Clean slate: the SO enforces at most one ACTIVE grant per
// (owner, spender, token), so revoke any leftover active grants for A or B from
// a previous run. This is what keeps the demo re-runnable, and it demonstrates
// the invariant: multiple merchants share one balance because each is a
// distinct spender with its own single active grant.
const merchantAHex = merchantAKey;
const merchantBHex = merchantBKey;
const preexisting = await ownerWallet.queryTokenAllowances({
  ownerPublicKey,
  tokenIdentifier,
});
for (const info of preexisting) {
  const spender = info.allowancePayload
    ? bytesToHex(info.allowancePayload.spenderPublicKey)
    : "";
  if (spender === merchantAHex || spender === merchantBHex) {
    await ownerWallet.revokeTokenAllowance(
      bytesToHex(info.allowancePayload!.allowanceId),
    );
    console.log(`Revoked leftover active grant for ${spender.slice(0, 12)}…`);
  }
}

const expiry = new Date(Date.now() + 60 * 60_000);

// 1. Grant both merchants against the same owner pool. Each is metered independently.
const { allowanceId: allowanceA } = await ownerWallet.createTokenAllowance({
  spenderPublicKey: merchantAKey,
  tokenIdentifier,
  perTransaction: { amount: 5_000n },
  total: { amount: 20_000n },
  expiryTime: expiry,
});
const { allowanceId: allowanceB } = await ownerWallet.createTokenAllowance({
  spenderPublicKey: merchantBKey,
  tokenIdentifier,
  perTransaction: { amount: 5_000n },
  total: { amount: 20_000n },
  expiryTime: expiry,
});
const ids = { A: allowanceA, B: allowanceB };
console.log(
  `\nGranted A=${allowanceA} and B=${allowanceB} against the same owner balance`,
);
await report("after grants", ids);

const activeForOwner = (
  await ownerWallet.queryTokenAllowances({ ownerPublicKey, tokenIdentifier })
).filter((a) => a.allowancePayload).length;
check(
  activeForOwner >= 2,
  `owner has >= 2 active allowances against one balance (saw ${activeForOwner})`,
);

async function pull(
  wallet: InstanceType<typeof SparkWallet>,
  allowanceId: string,
): Promise<string> {
  const prepared = await wallet.startAllowancePull({
    allowanceId,
    ownerPublicKey,
    tokenIdentifier,
    outputs: [{ receiverPublicKey: settlementKey, tokenAmount: pullAmount }],
  });
  const { txId } = await wallet.commitAllowancePull(prepared);
  return txId;
}

// 2. Merchant A pulls.
const ownerBefore = await tokenBalanceOf(ownerWallet);
const txA = await pull(merchantA, allowanceA);
console.log(`\nMerchant A pulled ${pullAmount} (tx ${txA})`);
const spentAAfterA = await spentOf(allowanceA);
const spentBAfterA = await spentOf(allowanceB);
await report("after A pulls", ids);
check(spentAAfterA > 0n, "A's spent advanced after A's pull");
check(spentBAfterA === 0n, "B's spent is still zero (independent meter)");

// 3. Merchant B pulls from the same owner pool.
const txB = await pull(merchantB, allowanceB);
console.log(`\nMerchant B pulled ${pullAmount} (tx ${txB})`);
const spentAAfterB = await spentOf(allowanceA);
const spentBAfterB = await spentOf(allowanceB);
await report("after B pulls", ids);
check(spentBAfterB > 0n, "B's spent advanced after B's pull");
check(
  spentAAfterB === spentAAfterA,
  "A's spent unchanged by B's pull (independent meters)",
);
const ownerAfterBoth = await tokenBalanceOf(ownerWallet);
check(
  ownerAfterBoth === ownerBefore - spentAAfterB - spentBAfterB,
  "owner balance dropped by the sum of both merchants' metered amounts",
);

// 4. Owner revokes Merchant A only.
// The SO orders create/revoke by the wallet-provided server timestamp and
// rejects a revoke that predates its grant. Running the whole lifecycle back to
// back can leave the two timestamps within the same millisecond window, so wait
// briefly to guarantee the revoke timestamp is strictly after the grant. (In
// the UI these are separate user actions seconds apart, so this never bites.)
await new Promise((resolve) => setTimeout(resolve, 2_000));
await ownerWallet.revokeTokenAllowance(allowanceA);
console.log(`\nOwner revoked Merchant A only`);
await report("after revoking A", ids);

// 5. Merchant A's next pull must fail with the typed revoked error.
try {
  await pull(merchantA, allowanceA);
  console.error("  FAIL: A's pull after revocation unexpectedly succeeded");
  failures++;
} catch (error) {
  check(
    tokenAllowanceFailureOf(error) === "REVOKED",
    `A's pull after revocation rejected with a REVOKED allowance failure (got ${
      error instanceof Error ? error.name : String(error)
    })`,
  );
}

// 6. Merchant B is unaffected and can still pull.
const spentBBeforeFinal = await spentOf(allowanceB);
const txB2 = await pull(merchantB, allowanceB);
console.log(`\nMerchant B pulled again ${pullAmount} (tx ${txB2})`);
const spentBFinal = await spentOf(allowanceB);
await report("final", ids);
check(
  spentBFinal > spentBBeforeFinal,
  "B's spent advanced again after A was revoked (B unaffected)",
);

await Promise.all([
  ownerWallet.cleanupConnections(),
  merchantA.cleanupConnections(),
  merchantB.cleanupConnections(),
  settlementWallet.cleanupConnections(),
]);

if (failures > 0) {
  console.error(`\nMulti-merchant demo FAILED (${failures} check(s))`);
  process.exit(1);
}
console.log(
  "\nMulti-merchant demo complete: two merchants shared one balance with independent meters; revoking one left the other spending.",
);
