/**
 * Integration tests for SparkReadonlyClient deposit and UTXO methods:
 *   - getUnusedDepositAddresses
 *   - getStaticDepositAddresses
 *   - getUtxosForDepositAddress
 *
 * Creates wallets with deposit addresses and queries them via the readonly client.
 */
import { describe, it, expect, jest, beforeAll } from "@jest/globals";
import {
  createEmptyWallet,
  createPublicReadonlyClient,
  createOwnerReadonlyClient,
  type FundedWallet,
} from "./helpers.js";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.node.js";
import { SparkWalletTestingWithStream } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { SparkValidationError } from "../../errors/types.js";

describe("getUnusedDepositAddresses", () => {
  jest.setTimeout(30_000);

  let walletInfo: FundedWallet;
  let publicClient: SparkReadonlyClient;
  let ownerClient: SparkReadonlyClient;

  beforeAll(async () => {
    const { wallet, mnemonic } = await SparkWalletTestingWithStream.initialize({
      options: { network: "LOCAL" },
    });

    // Generate a few single-use deposit addresses
    for (let i = 0; i < 3; i++) {
      await wallet.getSingleUseDepositAddress();
    }

    const sparkAddress = await wallet.getSparkAddress();
    const identityPublicKey = await wallet.getIdentityPublicKey();

    walletInfo = {
      wallet,
      sparkAddress,
      identityPublicKey,
      mnemonic: mnemonic!,
    };

    publicClient = createPublicReadonlyClient();
    ownerClient = await createOwnerReadonlyClient(walletInfo.mnemonic);
  });

  // ── Happy Paths ──────────────────────────────────────────

  it("returns unused deposit addresses via public client", async () => {
    const result = await publicClient.getUnusedDepositAddresses({
      sparkAddress: walletInfo.sparkAddress,
    });
    expect(result.depositAddresses.length).toBe(3);
  });

  it("returns unused deposit addresses via owner client", async () => {
    const result = await ownerClient.getUnusedDepositAddresses({
      sparkAddress: walletInfo.sparkAddress,
    });
    expect(result.depositAddresses.length).toBe(3);
  });

  it("respects limit parameter", async () => {
    const result = await publicClient.getUnusedDepositAddresses({
      sparkAddress: walletInfo.sparkAddress,
      limit: 1,
    });
    expect(result.depositAddresses.length).toBe(1);
  });

  it("respects offset parameter", async () => {
    const allResult = await publicClient.getUnusedDepositAddresses({
      sparkAddress: walletInfo.sparkAddress,
    });
    const offsetResult = await publicClient.getUnusedDepositAddresses({
      sparkAddress: walletInfo.sparkAddress,
      offset: 1,
    });
    // offsetResult should have fewer items
    expect(offsetResult.depositAddresses.length).toBe(
      allResult.depositAddresses.length - 1,
    );
  });

  it("returns empty for a wallet with no deposit addresses", async () => {
    const empty = await createEmptyWallet();
    const result = await publicClient.getUnusedDepositAddresses({
      sparkAddress: empty.sparkAddress,
    });
    expect(result.depositAddresses).toHaveLength(0);
  });

  // ── Unhappy Paths ────────────────────────────────────────

  it("rejects limit = 0", async () => {
    await expect(
      publicClient.getUnusedDepositAddresses({
        sparkAddress: walletInfo.sparkAddress,
        limit: 0,
      }),
    ).rejects.toThrow(SparkValidationError);
  });
});

describe("getStaticDepositAddresses", () => {
  jest.setTimeout(30_000);

  let publicClient: SparkReadonlyClient;

  beforeAll(() => {
    publicClient = createPublicReadonlyClient();
  });

  it("returns the static deposit address after one is generated", async () => {
    const { wallet, mnemonic } = await SparkWalletTestingWithStream.initialize({
      options: { network: "LOCAL" },
    });

    // Generate a static deposit address
    await wallet.getStaticDepositAddress();

    const sparkAddress = await wallet.getSparkAddress();
    const result = await publicClient.getStaticDepositAddresses(sparkAddress);
    expect(result.length).toBeGreaterThanOrEqual(1);
  });

  it("returns empty for a wallet with no static addresses", async () => {
    const empty = await createEmptyWallet();
    const result = await publicClient.getStaticDepositAddresses(
      empty.sparkAddress,
    );
    expect(result).toHaveLength(0);
  });

  it("throws on an invalid spark address", async () => {
    await expect(
      publicClient.getStaticDepositAddresses("not-a-valid-address"),
    ).rejects.toThrow();
  });
});

describe("getUtxosForDepositAddress", () => {
  jest.setTimeout(60_000);

  let publicClient: SparkReadonlyClient;

  beforeAll(() => {
    publicClient = createPublicReadonlyClient();
  });

  it("returns UTXOs for a deposit address with a confirmed transaction", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet } = await SparkWalletTestingWithStream.initialize({
      options: { network: "LOCAL" },
    });

    const depositAddress = await wallet.getSingleUseDepositAddress();
    await faucet.sendToAddress(depositAddress, 5_000n);
    await faucet.mineBlocksAndWaitForMiningToComplete(3);

    const result = await publicClient.getUtxosForDepositAddress({
      depositAddress,
    });
    expect(result.utxos.length).toBeGreaterThanOrEqual(1);
    expect(result.utxos[0]!.txid).toBeDefined();
    expect(result.utxos[0]!.vout).toBeDefined();
  });

  it("returns empty for a deposit address with no UTXOs", async () => {
    const { wallet } = await SparkWalletTestingWithStream.initialize({
      options: { network: "LOCAL" },
    });
    const depositAddress = await wallet.getSingleUseDepositAddress();

    const result = await publicClient.getUtxosForDepositAddress({
      depositAddress,
    });
    expect(result.utxos).toHaveLength(0);
  });
});
