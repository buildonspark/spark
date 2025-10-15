import { describe, expect, it } from "@jest/globals";
import { ConfigOptions } from "../../../services/wallet-config.js";
import { SparkWallet } from "../../../spark-wallet/spark-wallet.node.js";
import {
  CurrencyUnit,
  LightningReceiveRequestStatus,
} from "../../../types/index.js";
import { ValidationError } from "../../../errors/types.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { waitForClaim } from "../../utils/utils.js";

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
        name: ValidationError.name,
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
        name: ValidationError.name,
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
        name: ValidationError.name,
        message: expect.stringContaining("Invalid memo size"),
        context: expect.objectContaining({
          field: "memo",
          value: "test".repeat(1000),
        }),
      });
    }, 30000);
  });
});
