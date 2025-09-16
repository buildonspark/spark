import {
  initTestingWallet,
  SparkWalletTesting,
} from "../../utils/spark-testing-wallet.js";
import { sha256 } from "@noble/hashes/sha2";
import { bytesToHex } from "@noble/hashes/utils";
import { BitcoinFaucet } from "../../utils/test-faucet.js";

const DEPOSIT_AMOUNT = 10000n;

describe("SSP static deposit validation tests", () => {
  // it("should reject claiming deposits with insufficient confirmation", async () => {
  //   const faucet = BitcoinFaucet.getInstance();

  //   const { wallet: userWallet } = await SparkWalletTesting.initialize(
  //     {
  //       options: {
  //         network: "LOCAL",
  //       },
  //     },
  //     false,
  //   );

  //   const depositAddress = await userWallet.getStaticDepositAddress();
  //   expect(depositAddress).toBeDefined();

  //   const signedTx = await faucet.sendToAddress(
  //     depositAddress,
  //     DEPOSIT_AMOUNT,
  //     0,
  //   );

  //   expect(signedTx).toBeDefined();
  //   const transactionId = signedTx.id;

  //   await expect(
  //     userWallet.getClaimStaticDepositQuote(transactionId),
  //   ).rejects.toThrow("Transaction not found");

  //   const vout = await (userWallet as any).getDepositTransactionVout(
  //     transactionId,
  //   );

  //   expect(transactionId).toBeDefined();

  //   await new Promise((resolve) => setTimeout(resolve, 30000));

  //   const quote = await userWallet.getClaimStaticDepositQuote(
  //     transactionId,
  //     vout!,
  //   );

  //   expect(quote).toBeDefined();
  // }, 600000);

  it("should validate static deposit request parameters", async () => {
    const {
      wallet: userWallet,
      depositAddress,
      signedTx,
      vout,
      faucet,
    } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

    await new Promise((resolve) => setTimeout(resolve, 30000));

    expect(signedTx).toBeDefined();

    const transactionId = signedTx.id;

    // Invalid transaction ID
    await expect(
      userWallet.getClaimStaticDepositQuote("invalid-txid", vout!),
    ).rejects.toThrow(/InvalidInputException/);

    await new Promise((resolve) => setTimeout(resolve, 5000));

    // Valid transaction ID but not same as signedTx.id
    await expect(
      userWallet.getClaimStaticDepositQuote(
        bytesToHex(sha256("invalid-txid")),
        vout!,
      ),
    ).rejects.toThrow("Transaction not found");

    await new Promise((resolve) => setTimeout(resolve, 5000));

    // Missing output index
    await expect(
      userWallet.getClaimStaticDepositQuote(transactionId, vout! + 10),
    ).rejects.toThrow("UTXO is spent or not found.");

    await new Promise((resolve) => setTimeout(resolve, 5000));

    // Valid quote request for control
    const quote = await userWallet.getClaimStaticDepositQuote(
      transactionId,
      vout!,
    );
    expect(quote).toBeDefined();
    console.log(
      "Static deposit quote validation passed for correct parameters.",
    );

    await new Promise((resolve) => setTimeout(resolve, 10000));

    // Invalid claim: missing signature
    await expect(
      userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote!.creditAmountSats,
        outputIndex: vout!,
        sspSignature: "",
      }),
    ).rejects.toThrow(
      'Request ClaimStaticDeposit failed. [{"message":"Something went wrong."',
    );

    await new Promise((resolve) => setTimeout(resolve, 10000));

    // Invalid claim: invalid credit amount
    await expect(
      userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote!.creditAmountSats + 1000,
        outputIndex: vout!,
        sspSignature: quote!.signature,
      }),
    ).rejects.toThrow(
      "The utxo amount is not enough to cover the claim amount",
    );

    await new Promise((resolve) => setTimeout(resolve, 10000));

    // Invalid claim: wrong output index
    await expect(
      userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote!.creditAmountSats,
        outputIndex: vout! + 10,
        sspSignature: quote!.signature,
      }),
    ).rejects.toThrow("UTXO is spent or not found.");
  }, 600000);
});
