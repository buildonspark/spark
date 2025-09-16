import { describe, expect, it } from "@jest/globals";
import { ConfigOptions } from "../../../services/wallet-config.js";
import { SparkWallet } from "../../../spark-wallet/spark-wallet.node.js";
import {
  BitcoinNetwork,
  CurrencyUnit,
  LightningReceiveRequestStatus,
} from "../../../types/index.js";
import { NetworkType } from "../../../utils/network.js";
import { ValidationError } from "../../../errors/types.js";

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
