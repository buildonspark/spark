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

describe("SSP unilateral exit — spark transfer", () => {
  it("should unilateral exit a received spark transfer leaf", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const senderWallet = await createClaimedWallet(faucet, DEPOSIT_AMOUNT);
    const receiverWallet = await initializeWalletWithConnectedStream();

    try {
      await senderWallet.transfer({
        amountSats: TRANSFER_AMOUNT,
        receiverSparkAddress: await receiverWallet.getSparkAddress(),
      });

      await waitForWalletBalance(receiverWallet, BigInt(TRANSFER_AMOUNT));
      await unilateralExitLargestLeaf(
        faucet,
        receiverWallet,
        BigInt(TRANSFER_AMOUNT),
      );
    } finally {
      await closeWallets(senderWallet, receiverWallet);
    }
  }, 300_000);
});
