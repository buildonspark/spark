import { describe, expect, it, jest } from "@jest/globals";

import SspClient from "../../../graphql/client.js";
import { ConfigOptions } from "../../../services/wallet-config.js";
import { SparkWalletTesting } from "../../utils/spark-testing-wallet.js";
import { AuthenticationError } from "../../../errors/types.js";

const options: ConfigOptions = {
  network: "LOCAL",
};

describe("SSP Auth Test", () => {
  it("Should authenticate successfully", async () => {
    const { wallet } = await SparkWalletTesting.initialize({
      options,
    });

    const res = await wallet.createLightningInvoice({
      amountSats: 1000,
    });

    expect(res.invoice.encodedInvoice).toBeDefined();
  });

  it("Should throw an error if the user is not authenticated", async () => {
    // Mock the authenticate function so we don't set the auth token
    const originalAuthenticate = SspClient.prototype.authenticate;
    SspClient.prototype.authenticate = jest.fn(async () => {});

    try {
      const { wallet } = await SparkWalletTesting.initialize({
        options,
      });

      await expect(
        wallet.createLightningInvoice({
          amountSats: 1000,
        }),
      ).rejects.toMatchObject({
        name: AuthenticationError.name,
        context: expect.objectContaining({
          endpoint: "graphql",
        }),
      });
    } finally {
      SspClient.prototype.authenticate = originalAuthenticate;
    }
  });

  it("Should reauthenticate successfully", async () => {
    // Mock the authenticate function so we don't set the auth token
    const originalAuthenticate = SspClient.prototype.authenticate;
    SspClient.prototype.authenticate = jest.fn(async () => {});

    const { wallet } = await SparkWalletTesting.initialize({
      options,
    });

    SspClient.prototype.authenticate = originalAuthenticate;

    const res = await wallet.createLightningInvoice({
      amountSats: 1000,
    });

    expect(res.invoice.encodedInvoice).toBeDefined();
  });
});
