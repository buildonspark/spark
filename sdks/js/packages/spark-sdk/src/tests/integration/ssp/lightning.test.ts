import { describe, expect, it } from "@jest/globals";
import { SparkValidationError } from "../../../errors/types.js";
import { decodeInvoice } from "../../../services/bolt11-spark.js";
import { type ConfigOptions } from "../../../services/wallet-config.js";
import { SparkWallet } from "../../../spark-wallet/spark-wallet.node.js";
import {
  CurrencyUnit,
  LightningReceiveRequestStatus,
  type LightningSendRequest,
  LightningSendRequestStatus,
} from "../../../types/index.js";
import {
  decodeSparkAddress,
  type SparkAddressFormat,
  validateSparkInvoiceSignature,
} from "../../../utils/address.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import {
  retryUntilSuccess,
  waitForBalance,
  waitForClaim,
} from "../../utils/utils.js";

const DEPOSIT_AMOUNT = 10000n;
const INVOICE_AMOUNT = 1000;

const options: ConfigOptions = {
  network: "LOCAL",
};
const { wallet: walletStatic } = await SparkWallet.initialize({
  mnemonicOrSeed:
    "logic ripple layer execute smart disease marine hero monster talent crucial unfair horror shadow maze abuse avoid story loop jaguar sphere trap decrease turn",
  options,
});

describe("Lightning Network provider", () => {
  describe("should create lightning invoice", () => {
    test.concurrent.each([
      [0],
      [1],
      [10],
      [4260],
      [100000000000],
      [100000000001],
    ])(
      `.amount(%s)`,
      async (amountSats) => {
        const invoice = await walletStatic.createLightningInvoice({
          amountSats: amountSats,
          memo: "test",
          expirySeconds: 500,
        });

        expect(invoice).toBeDefined();
        expect(invoice.invoice).toBeDefined();
        expect(invoice.invoice.encodedInvoice.length).toBeGreaterThanOrEqual(
          401,
        );
        expect(invoice.invoice.paymentHash.length).toEqual(64);
        expect(invoice.invoice.amount.originalValue).toEqual(amountSats * 1000);
        expect(invoice.invoice.amount.originalUnit).toEqual(
          CurrencyUnit.MILLISATOSHI,
        );
        expect(invoice.status).toEqual(
          LightningReceiveRequestStatus.INVOICE_CREATED,
        );
        expect(invoice.transfer).toBeUndefined();
      },
      30000,
    );
  });

  describe("should pay lightning invoice", () => {
    it("should pay lightning invoice created by another wallet", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "test",
        expirySeconds: 500,
      });

      expect(invoice).toBeDefined();

      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      const request = (await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      })) as LightningSendRequest;

      // wait for the claim event, we care about the transfer completing...
      await bobClaimed;

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBeLessThan(
        DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT),
      );

      // Verify that payment preimage is still set for spark -> spark lightning payments
      const lightningSendRequest = await aliceWallet.getLightningSendRequest(
        request.id,
      );
      expect(lightningSendRequest?.paymentPreimage).toBeDefined();
    }, 120000);
  });

  describe("should fail to create lightning invoice", () => {
    it(`should fail to create lightning invoice with invalid amount`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: -1,
          memo: "test",
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("Invalid amount"),
        context: expect.objectContaining({
          field: "amountSats",
          value: -1,
        }),
      });
    }, 30000);

    it(`should fail to create lightning invoice with invalid expiration time`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: 1000,
          memo: "test",
          expirySeconds: -1,
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("Invalid expiration time"),
        context: expect.objectContaining({
          field: "expirySeconds",
          value: -1,
        }),
      });
    }, 30000);

    it(`should fail to create lightning invoice with invalid memo size`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: 1000,
          memo: "test".repeat(1000),
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("Invalid memo size"),
        context: expect.objectContaining({
          field: "memo",
          value: "test".repeat(1000),
        }),
      });
    }, 30000);

    it(`should fail when both includeSparkAddress and includeSparkInvoice are true`, async () => {
      await expect(
        walletStatic.createLightningInvoice({
          amountSats: 1000,
          memo: "test",
          expirySeconds: 300,
          includeSparkAddress: true,
          includeSparkInvoice: true,
        }),
      ).rejects.toMatchObject({
        name: SparkValidationError.name,
        message: expect.stringContaining("mutually exclusive"),
        context: expect.objectContaining({
          field: "includeSparkInvoice",
        }),
      });
    }, 30000);
  });

  describe("should create lightning invoice with embedded spark invoice", () => {
    it("should embed spark invoice in fallback address", async () => {
      const invoice = await walletStatic.createLightningInvoice({
        amountSats: 5000,
        memo: "test spark invoice roundtrip",
        expirySeconds: 600,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);

      // Verify spark invoice is present and valid
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;

      expect(sparkInvoice).toMatch(/^(spark[lrts]?|sp[lrts]?)1/);

      // The spark invoice should be reasonable length (150-300 bytes typical)
      expect(sparkInvoice.length).toBeGreaterThan(50);
      expect(sparkInvoice.length).toBeLessThan(400);
    }, 30000);

    it("should pay invoice with embedded spark invoice using preferSpark", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob creates invoice with embedded spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "test preferSpark with spark invoice",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      expect(invoice).toBeDefined();

      // Verify spark invoice is embedded
      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      expect(decodedInvoice.fallbackAddress).toMatch(
        /^(spark[lrts]?|sp[lrts]?)1/,
      );

      // Alice pays with preferSpark - should use embedded spark invoice
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
      });

      await bobClaimed;

      // Verify Bob received the payment
      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      // Verify Alice's balance decreased (no Lightning fees when using Spark)
      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBe(DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT));
    }, 120000);

    it("should pay zero-amount lightning invoice with embedded zero-amount spark invoice using preferSpark", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob creates zero-amount invoice with embedded spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: 0,
        memo: "test zero-amount invoice with spark invoice",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      expect(invoice).toBeDefined();

      // Verify spark invoice is embedded and is zero-amount
      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      expect(decodedInvoice.fallbackAddress).toMatch(
        /^(spark[lrts]?|sp[lrts]?)1/,
      );
      expect(decodedInvoice.amountMSats).toBe(null);

      const paymentAmount = 5000;

      // Alice pays zero-amount invoice with preferSpark and amountSatsToSend
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
        amountSatsToSend: paymentAmount,
      });

      await bobClaimed;

      // Verify Bob received the payment
      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(paymentAmount));

      // Verify Alice's balance decreased
      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBe(DEPOSIT_AMOUNT - BigInt(paymentAmount));
    }, 120000);
  });

  describe("should validate zero-amount invoice matching", () => {
    it("should successfully pay zero-amount lightning invoice with zero-amount embedded spark invoice", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob creates zero-amount lightning invoice with embedded zero-amount spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: 0,
        memo: "zero-amount test",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.amountMSats).toBe(null);
      expect(decodedInvoice.fallbackAddress).toBeDefined();

      const paymentAmount = 3000;

      // Paying with preferSpark should validate that both invoices are zero-amount
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
        amountSatsToSend: paymentAmount,
      });

      await bobClaimed;

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(paymentAmount));
    }, 120000);

    it("should successfully pay non-zero lightning invoice with matching non-zero embedded spark invoice", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice's wallet
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      const invoiceAmount = 2000;

      // Bob creates non-zero lightning invoice with embedded matching non-zero spark invoice
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: invoiceAmount,
        memo: "non-zero matching test",
        expirySeconds: 300,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.amountMSats).toBe(BigInt(invoiceAmount * 1000));
      expect(decodedInvoice.fallbackAddress).toBeDefined();

      // Paying with preferSpark should validate that amounts match
      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bobWallet });
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
      });

      await bobClaimed;

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(invoiceAmount));
    }, 120000);
  });

  describe("should create lightning invoice with receiverIdentityPubkey", () => {
    it("should create signed spark invoice when receiverIdentityPubkey is not provided", async () => {
      const { wallet } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const invoice = await wallet.createLightningInvoice({
        amountSats: 5000,
        memo: "test receiverIdentityPubkey default",
        expirySeconds: 600,
        includeSparkInvoice: true,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;
      expect(invoice.sparkInvoice).toBe(sparkInvoice);

      const decodedSparkInvoice = decodeSparkAddress(sparkInvoice, "LOCAL");

      const creatorIdentityPubkey = await wallet.getIdentityPublicKey();

      expect(decodedSparkInvoice.identityPublicKey).toBe(creatorIdentityPubkey);
      expect(decodedSparkInvoice.signature).toBeDefined();

      validateSparkInvoiceSignature(sparkInvoice as SparkAddressFormat);
    }, 30000);

    it("should create signed spark invoice when receiverIdentityPubkey matches creator", async () => {
      const { wallet } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const creatorIdentityPubkey = await wallet.getIdentityPublicKey();

      const invoice = await wallet.createLightningInvoice({
        amountSats: 5000,
        memo: "test receiverIdentityPubkey same as creator",
        expirySeconds: 600,
        includeSparkInvoice: true,
        receiverIdentityPubkey: creatorIdentityPubkey,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;
      expect(invoice.sparkInvoice).toBe(sparkInvoice);

      const decodedSparkInvoice = decodeSparkAddress(sparkInvoice, "LOCAL");

      expect(decodedSparkInvoice.identityPublicKey).toBe(creatorIdentityPubkey);
      expect(decodedSparkInvoice.signature).toBeDefined();

      validateSparkInvoiceSignature(sparkInvoice as SparkAddressFormat);
    }, 30000);

    it("should create unsigned spark invoice when receiverIdentityPubkey differs from creator", async () => {
      const { wallet: creatorWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const { wallet: receiverWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const creatorIdentityPubkey = await creatorWallet.getIdentityPublicKey();
      const receiverIdentityPubkey =
        await receiverWallet.getIdentityPublicKey();

      expect(creatorIdentityPubkey).not.toBe(receiverIdentityPubkey);

      const invoice = await creatorWallet.createLightningInvoice({
        amountSats: 5000,
        memo: "test receiverIdentityPubkey different from creator",
        expirySeconds: 600,
        includeSparkInvoice: true,
        receiverIdentityPubkey: receiverIdentityPubkey,
      });

      const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
      expect(decodedInvoice.fallbackAddress).toBeDefined();
      const sparkInvoice = decodedInvoice.fallbackAddress!;
      expect(invoice.sparkInvoice).toBe(sparkInvoice);

      const decodedSparkInvoice = decodeSparkAddress(sparkInvoice, "LOCAL");

      expect(decodedSparkInvoice.identityPublicKey).toBe(
        receiverIdentityPubkey,
      );
      expect(decodedSparkInvoice.signature).toBeUndefined();

      expect(() =>
        validateSparkInvoiceSignature(sparkInvoice as SparkAddressFormat),
      ).toThrow(SparkValidationError);
    }, 30000);
  });

  describe("creating an invoice with receiverIdentityPubkey", () => {
    it("should successfully create and pay an invoice with receiverIdentityPubkey", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: alice } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const { wallet: bob } = await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });

      const depositAddress = await alice.getSingleUseDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocksAndWaitForMiningToComplete(6);

      await alice.claimDeposit(signedTx.id);
      await waitForBalance(alice, DEPOSIT_AMOUNT);

      const invoice = await alice.createLightningInvoice({
        amountSats: 1000,
        memo: "test invoice",
        expirySeconds: 600,
        receiverIdentityPubkey: await bob.getIdentityPublicKey(),
      });

      expect(invoice).toBeDefined();

      // Register listener before payment so we don't miss the stream event.
      const bobClaimed = waitForClaim({ wallet: bob });
      await alice.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      });

      await bobClaimed;

      const { balance: bobBalance } = await bob.getBalance();
      expect(bobBalance).toBe(BigInt(1000));
    }, 120_000);
  });

  // A Spark wallet paying another Spark wallet's locally-issued invoice is an
  // "internal" payment: both legs live on this SSP. The SSP may serve it via the
  // unified internal-payment state machine (when the sparkcore knob
  // spark.ssp.internal_lightning_payment.state_machine.enabled is on) or via the
  // legacy split send/receive flow (when off). This test asserts only the
  // user-visible outcome — the receiver is paid and the sender's send request
  // reaches TRANSFER_COMPLETED — so it must pass identically in both knob states.
  //
  // The sender-status assertion is the load-bearing part. The internal flow never
  // advances the SparkLightningSendRequest row itself (the internal-payment row is
  // the source of truth), so the send request's status must be *surfaced* from
  // that row. Without that surfacing the send request sits at CREATED forever even
  // after the payment settles, and this poll times out — which is exactly why the
  // status surfacing must land before the knob is enabled. Internal COMPLETED and
  // the legacy flow both map onto TRANSFER_COMPLETED, so the assertion is
  // knob-agnostic.
  describe("internal (Spark-to-Spark) lightning payment", () => {
    it("pays a locally-issued Spark invoice; receiver is paid and sender request completes", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });
      const { wallet: bobWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      // Fund Alice so she has leaves to pay with.
      const depositAddress = await aliceWallet.getSingleUseDepositAddress();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocksAndWaitForMiningToComplete(6);
      await aliceWallet.claimDeposit(signedTx.id);
      await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

      // Bob issues a locally-issued Spark invoice (SIGNING_OPERATOR_SWAP
      // receive), which is what makes Alice's payment eligible for the internal
      // flow.
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "internal payment test",
        expirySeconds: 500,
      });
      expect(invoice.status).toEqual(
        LightningReceiveRequestStatus.INVOICE_CREATED,
      );

      // Register the receiver's claim listener before paying so we don't miss it.
      // throwOnTimeout so a missing claim surfaces as a clear timeout rather than a
      // misleading "expected 1000n, got 0n" balance assertion downstream.
      const bobClaimed = waitForClaim({
        wallet: bobWallet,
        throwOnTimeout: true,
      });

      const payResult = await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      });

      // Paying over Lightning (no preferSpark, no embedded spark invoice)
      // returns the SparkLightningSendRequest, not a settled Spark WalletTransfer
      // (which uniquely carries transferDirection).
      expect("transferDirection" in payResult).toBe(false);
      const sendRequest = payResult as LightningSendRequest;
      expect(sendRequest.id).toBeDefined();

      // Receiver leg: Bob is credited the invoice amount.
      await bobClaimed;
      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      // Sender leg: the send request surfaces terminal success. TRANSFER_COMPLETED
      // is reached by both the legacy flow and the internal flow, so this holds
      // regardless of the knob.
      const completed = await retryUntilSuccess(
        async () => {
          const req = await aliceWallet.getLightningSendRequest(sendRequest.id);
          if (req?.status !== LightningSendRequestStatus.TRANSFER_COMPLETED) {
            throw new Error(
              `send request ${sendRequest.id} not complete yet: ${req?.status}`,
            );
          }
          return req;
        },
        { maxAttempts: 30, delayMs: 2000 },
      );
      expect(completed.status).toEqual(
        LightningSendRequestStatus.TRANSFER_COMPLETED,
      );

      // Sender was debited the payment amount (plus the SSP fee).
      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBeLessThan(
        DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT),
      );
    }, 180_000);
  });
});
