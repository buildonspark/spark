import { describe, expect, it } from "@jest/globals";
import { TransferType, transferTypeToJSON } from "../../../proto/spark.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";

const DEPOSIT_AMOUNT = 10000n;

async function fundWalletWithStaticDeposit(
  wallet: Awaited<
    ReturnType<typeof SparkWalletTestingWithStream.initialize>
  >["wallet"],
  amount: bigint = DEPOSIT_AMOUNT,
): Promise<bigint> {
  const faucet = BitcoinFaucet.getInstance();
  const depositAddress = await wallet.getStaticDepositAddress();
  expect(depositAddress).toBeDefined();

  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  await faucet.mineBlocks(6);

  expect(signedTx).toBeDefined();

  const transactionId = signedTx.id;

  let vout;
  for (let i = 0; i < signedTx.outputsLength; i++) {
    const output = signedTx.getOutput(i);
    if (output.amount === amount) {
      vout = i;
      break;
    }
  }

  const quote = await wallet.getClaimStaticDepositQuote(transactionId, vout!);

  const quoteAmount = quote!.creditAmountSats;
  const sspSignature = quote!.signature;

  await wallet.claimStaticDeposit({
    transactionId,
    creditAmountSats: quoteAmount,
    sspSignature,
    outputIndex: vout!,
  });

  await new Promise((resolve) => setTimeout(resolve, 1000));

  const { balance } = await wallet.getBalance();
  expect(balance).toBe(BigInt(quoteAmount));

  return BigInt(quoteAmount);
}

describe("SSP Transfers Test", () => {
  it("getTransfers and getTransfer should return the corresponding ssp request if it exists", async () => {
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

    const quoteAmount = await fundWalletWithStaticDeposit(userWallet);

    await userWallet.transfer({
      amountSats: Number(quoteAmount),
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

  it("getTransfers and getTransfer should return sparkInvoice for invoice-tied transfers", async () => {
    const { wallet: senderWallet } =
      await SparkWalletTestingWithStream.initialize({
        options: {
          network: "LOCAL",
        },
      });

    const { wallet: receiverWallet } =
      await SparkWalletTestingWithStream.initialize({
        options: {
          network: "LOCAL",
        },
      });

    await fundWalletWithStaticDeposit(senderWallet);

    const invoiceAmount = 1000;
    const tomorrow = new Date(Date.now() + 1000 * 60 * 60 * 24);
    const sparkInvoice = await receiverWallet.createSatsInvoice({
      amount: invoiceAmount,
      memo: "Test invoice",
      expiryTime: tomorrow,
      senderSparkAddress: await senderWallet.getSparkAddress(),
    });

    const transferResults = await senderWallet.fulfillSparkInvoice([
      { invoice: sparkInvoice },
    ]);

    expect(transferResults.satsTransactionSuccess.length).toBe(1);
    expect(transferResults.satsTransactionErrors.length).toBe(0);

    const transferId =
      transferResults.satsTransactionSuccess[0]!.transferResponse.id;

    const invoiceTransfer = await senderWallet.getTransfer(transferId);

    expect(invoiceTransfer).toBeDefined();
    expect(invoiceTransfer?.sparkInvoice).toBe(sparkInvoice);
    expect(invoiceTransfer?.type).toEqual(
      transferTypeToJSON(TransferType.TRANSFER),
    );

    const retrievedTransfer = await receiverWallet.getTransfer(transferId);
    expect(retrievedTransfer).toBeDefined();
    expect(retrievedTransfer?.sparkInvoice).toBe(sparkInvoice);
  }, 60000);
});
