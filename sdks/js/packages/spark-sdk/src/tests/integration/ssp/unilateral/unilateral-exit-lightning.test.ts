import { describe, it } from "@jest/globals";

import { BitcoinFaucet } from "../../../utils/test-faucet.js";
import {
  closeWallets,
  createClaimedWallet,
  DEPOSIT_AMOUNT,
  initializeWalletWithConnectedStream,
  TRANSFER_AMOUNT,
  unilateralExitLargestLeaf,
  waitForWalletBalance,
} from "./shared.js";

describe("SSP unilateral exit — lightning receive", () => {
  it("should unilateral exit a received lightning payment leaf", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const aliceWallet = await createClaimedWallet(faucet, DEPOSIT_AMOUNT);
    const bobWallet = await initializeWalletWithConnectedStream();

    try {
      const invoice = await bobWallet.createLightningInvoice({
        amountSats: TRANSFER_AMOUNT,
        memo: "ssp unilateral exit integration test",
        expirySeconds: 500,
      });

      await aliceWallet.payLightningInvoice({
        invoice: invoice.invoice.encodedInvoice,
        maxFeeSats: 100,
      });
      await waitForWalletBalance(bobWallet, BigInt(TRANSFER_AMOUNT));

      await unilateralExitLargestLeaf(
        faucet,
        bobWallet,
        BigInt(TRANSFER_AMOUNT),
      );
    } finally {
      await closeWallets(aliceWallet, bobWallet);
    }
  }, 300_000);
});
