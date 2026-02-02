import { describe, expect, it } from "@jest/globals";
import { ConfigOptions } from "../../../services/wallet-config.js";
import { SparkWallet } from "../../../spark-wallet/spark-wallet.node.js";
import {
  CurrencyUnit,
  LightningReceiveRequestStatus,
} from "../../../types/index.js";
import { SparkValidationError } from "../../../errors/types.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { waitForClaim } from "../../utils/utils.js";
import { decodeInvoice } from "../../../services/bolt11-spark.js";

const DEPOSIT_AMOUNT = 10000n;
const INVOICE_AMOUNT = 1000;

const options: ConfigOptions = {
  network: "LOCAL",
};
const { wallet: walletStatic, ...rest } = await SparkWallet.initialize({
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
        let invoice = await walletStatic.createLightningInvoice({
          amountSats: amountSats,
          memo: "test",
          expirySeconds: 10,
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

      await waitForClaim({ wallet: aliceWallet });

      const { balance } = await aliceWallet.getBalance();
      expect(balance).toBe(DEPOSIT_AMOUNT);

      const invoice = await bobWallet.createLightningInvoice({
        amountSats: INVOICE_AMOUNT,
        memo: "test",
        expirySeconds: 10,
      });

      expect(invoice).toBeDefined();

      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      });

      await waitForClaim({ wallet: bobWallet });

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

      const { balance: aliceBalance } = await aliceWallet.getBalance();
      expect(aliceBalance).toBeLessThan(
        DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT),
      );
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
      await waitForClaim({ wallet: aliceWallet });

      const { balance: aliceInitialBalance } = await aliceWallet.getBalance();
      expect(aliceInitialBalance).toBe(DEPOSIT_AMOUNT);

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
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
      });

      await waitForClaim({ wallet: bobWallet });

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
      await waitForClaim({ wallet: aliceWallet });

      const { balance: aliceInitialBalance } = await aliceWallet.getBalance();
      expect(aliceInitialBalance).toBe(DEPOSIT_AMOUNT);

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
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
        amountSatsToSend: paymentAmount,
      });

      await waitForClaim({ wallet: bobWallet });

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
      await waitForClaim({ wallet: aliceWallet });

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
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
        amountSatsToSend: paymentAmount,
      });

      await waitForClaim({ wallet: bobWallet });

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
      await waitForClaim({ wallet: aliceWallet });

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
      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
        preferSpark: true,
      });

      await waitForClaim({ wallet: bobWallet });

      const { balance: bobBalance } = await bobWallet.getBalance();
      expect(bobBalance).toBe(BigInt(invoiceAmount));
    }, 120000);
  });
});
