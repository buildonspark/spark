/**
 * Token allowance negative-path demo (Spark Pull, token engine).
 *
 * Exercises the two spend-limit guardrails live against a running local Spark
 * stack (run-everything.sh), asserting the typed SDK errors surface:
 *
 *   A. Per-transaction cap: a single pull whose metered amount (principal)
 *      exceeds the allowance per-transaction cap must fail with
 *      a PER_TRANSACTION_CAP_EXCEEDED allowance failure.
 *
 *   B. Budget exhaustion: successive pulls that consume the allowance's total
 *      limit succeed until the next pull's metered amount exceeds the
 *      remaining budget, which must fail with
 *      a BUDGET_EXHAUSTED allowance failure.
 *
 * The process exits 0 only when both guardrails fire as expected.
 *
 * Environment mirrors token-allowance-pull.ts (OWNER/SPENDER/MERCHANT
 * mnemonics, MINT_AMOUNT, NETWORK, etc.). Run with NUM_SPARK_OPERATORS matching
 * the local operator count and SPARK_DANGEROUSLY_DISABLE_TLS_VERIFICATION=1 for
 * the self-signed local certs.
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

console.log(`Token allowance NEGATIVE-path demo on ${network}`);

const { wallet: ownerWallet } = await IssuerSparkWallet.initialize({
  mnemonicOrSeed: process.env["OWNER_MNEMONIC"],
  options,
});
const { wallet: spenderWallet } = await SparkWallet.initialize({
  mnemonicOrSeed: process.env["SPENDER_MNEMONIC"],
  options,
});
const { wallet: merchantWallet } = await SparkWallet.initialize({
  mnemonicOrSeed: process.env["MERCHANT_MNEMONIC"],
  options,
});

const ownerPublicKey = await ownerWallet.getIdentityPublicKey();
const spenderPublicKey = await spenderWallet.getIdentityPublicKey();
const merchantPublicKey = await merchantWallet.getIdentityPublicKey();

// Token + supply.
let bech32mTokenIdentifier: Bech32mTokenIdentifier;
const existingTokens = await ownerWallet.getIssuerTokensMetadata();
if (existingTokens.length > 0) {
  bech32mTokenIdentifier = existingTokens[0].bech32mTokenIdentifier;
} else {
  const creation = await ownerWallet.createToken({
    tokenName: "PullNegDemoToken",
    tokenTicker: "PULLN",
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

// The SO allows at most one active grant per (owner, spender, token), so revoke
// any grant left active by an interrupted earlier run before re-exercising the
// guardrails.
const spenderHex = spenderPublicKey;
const preexisting = await ownerWallet.queryTokenAllowances({
  ownerPublicKey,
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

const expiry = new Date(Date.now() + 60 * 60_000);
let failures = 0;

// ---------------------------------------------------------------------------
// A. Per-transaction cap exceeded.
// perTxCap=1000; a 2000-principal pull meters well above the cap.
// ---------------------------------------------------------------------------
{
  const { allowanceId } = await ownerWallet.createTokenAllowance({
    spenderPublicKey,
    tokenIdentifier,
    perTransaction: { amount: 1_000n },
    total: { amount: 100_000n },
    expiryTime: expiry,
  });
  console.log(
    `\n[A] per-tx-cap allowance ${allowanceId} (cap=1000), pulling 2000`,
  );
  try {
    const prepared = await spenderWallet.startAllowancePull({
      allowanceId,
      ownerPublicKey,
      tokenIdentifier,
      outputs: [{ receiverPublicKey: merchantPublicKey, tokenAmount: 2_000n }],
    });
    await spenderWallet.commitAllowancePull(prepared);
    console.error("[A] ERROR: pull above per-tx cap unexpectedly succeeded");
    failures++;
  } catch (error) {
    const failure = tokenAllowanceFailureOf(error);
    if (failure === "PER_TRANSACTION_CAP_EXCEEDED") {
      console.log(`[A] PASS: got ${failure}`);
    } else {
      console.error(`[A] ERROR: unexpected error type: ${String(error)}`);
      failures++;
    }
  }
  await ownerWallet.revokeTokenAllowance(allowanceId);
}

// ---------------------------------------------------------------------------
// B. Budget exhaustion across successive pulls.
// perTxCap=1500 (<= totalLimit), totalLimit=3000. Each 1000-token pull meters
// its principal, so successive pulls draw the budget down until the next pull
// exceeds the remaining budget.
// ---------------------------------------------------------------------------
{
  const { allowanceId } = await ownerWallet.createTokenAllowance({
    spenderPublicKey,
    tokenIdentifier,
    perTransaction: { amount: 1_500n },
    total: { amount: 3_000n },
    expiryTime: expiry,
  });
  console.log(
    `\n[B] budget allowance ${allowanceId} (totalLimit=3000), pulling 1000 repeatedly`,
  );
  let succeeded = 0;
  let exhausted = false;
  for (let attempt = 1; attempt <= 5; attempt++) {
    try {
      const prepared = await spenderWallet.startAllowancePull({
        allowanceId,
        ownerPublicKey,
        tokenIdentifier,
        outputs: [
          { receiverPublicKey: merchantPublicKey, tokenAmount: 1_000n },
        ],
      });
      const { txId } = await spenderWallet.commitAllowancePull(prepared);
      succeeded++;
      console.log(`[B] pull #${attempt} of 1000 settled (tx ${txId})`);
    } catch (error) {
      const failure = tokenAllowanceFailureOf(error);
      if (failure === "BUDGET_EXHAUSTED") {
        console.log(
          `[B] PASS: pull #${attempt} rejected with ${failure} after ${succeeded} successful pull(s)`,
        );
        exhausted = true;
        break;
      }
      console.error(`[B] ERROR: unexpected error type: ${String(error)}`);
      failures++;
      break;
    }
  }
  if (!exhausted) {
    console.error("[B] ERROR: budget was never reported exhausted");
    failures++;
  }
  if (succeeded !== 3) {
    console.error(
      `[B] ERROR: expected 3 settled pulls before exhaustion, got ${succeeded}`,
    );
    failures++;
  }
  await ownerWallet.revokeTokenAllowance(allowanceId);
}

await Promise.all([
  ownerWallet.cleanupConnections(),
  spenderWallet.cleanupConnections(),
  merchantWallet.cleanupConnections(),
]);

if (failures > 0) {
  console.error(`\nNegative-path demo FAILED (${failures} check(s))`);
  process.exit(1);
}
console.log("\nNegative-path demo complete: both guardrails fired as expected");
