import { LightningService } from "../services/lightning";
import { getTestWalletConfig } from "./test-util";
import { ConnectionManager } from "../services/connection";
import { WalletConfigService } from "../services/config";

describe("LightningService", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn("should create an invoice", async () => {
    const config = getTestWalletConfig();
    const lightningService = new LightningService(
      new WalletConfigService(config),
      new ConnectionManager()
    );

    const invoice = await lightningService.createLightningInvoice({
      invoiceCreator: async () => "fake-invoice",
      amountSats: 100,
      memo: "test",
    });

    expect(invoice).toBeDefined();
  });
});
