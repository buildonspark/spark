import { describe, expect, it } from "@jest/globals";
import { TransferType, transferTypeToJSON } from "../../../proto/spark.js";
import {
  SparkWalletTesting,
  SparkWalletTestingWithStream,
} from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";

const DEPOSIT_AMOUNT = 10000n;
const SECOND_DEPOSIT_AMOUNT = 20000n;

describe("SSP Transfers Test", () => {
  it("getTransfers and getTransfer should return the corresponding ssp request if it exists", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: userWallet } =
      await SparkWalletTestingWithStream.initialize({
        options: {
          network: "LOCAL",
        },
      });

    const { wallet: userWallet2 } =
      await SparkWalletTestingWithStream.initialize({
        options: {
          network: "LOCAL",
        },
      });

    const depositAddress = await userWallet.getStaticDepositAddress();
    expect(depositAddress).toBeDefined();

    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);

    // Wait for the transaction to be mined
    await faucet.mineBlocks(6);

    expect(signedTx).toBeDefined();

    const transactionId = signedTx.id;

    let vout;

    for (let i = 0; i < signedTx.outputsLength; i++) {
      const output = signedTx.getOutput(i);
      if (output.amount === DEPOSIT_AMOUNT) {
        vout = i;
        break;
      }
    }

    const quote = await userWallet.getClaimStaticDepositQuote(
      transactionId,
      vout!,
    );

    const quoteAmount = quote!.creditAmountSats;
    const sspSignature = quote!.signature;

    await userWallet.claimStaticDeposit({
      transactionId,
      creditAmountSats: quoteAmount,
      sspSignature,
      outputIndex: vout!,
    });

    await new Promise((resolve) => setTimeout(resolve, 1000));

    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(BigInt(quoteAmount));

    await userWallet.transfer({
      amountSats: quoteAmount,
      receiverSparkAddress: await userWallet2.getSparkAddress(),
    });

    const transfers = await userWallet.getTransfers();
    expect(transfers.transfers.length).toBe(2);

    const firstTransfer = transfers.transfers[0];
    expect(firstTransfer).toBeDefined();
    expect(firstTransfer?.userRequest).not.toBeDefined();
    expect(firstTransfer?.type).toEqual(
      transferTypeToJSON(TransferType.TRANSFER),
    );

    const sparkTransfer = await userWallet.getTransfer(firstTransfer!.id);
    expect(sparkTransfer?.userRequest).not.toBeDefined();

    const secondTransfer = transfers.transfers[1];
    expect(secondTransfer).toBeDefined();
    expect(secondTransfer?.userRequest).toBeDefined();
    expect(secondTransfer?.type).toEqual(
      transferTypeToJSON(TransferType.UTXO_SWAP),
    );
    expect(secondTransfer?.userRequest?.typename).toBe("ClaimStaticDeposit");

    const utxoSwapTransfer = await userWallet.getTransfer(secondTransfer!.id);
    expect(utxoSwapTransfer?.userRequest).toBeDefined();
  }, 60000);
});
