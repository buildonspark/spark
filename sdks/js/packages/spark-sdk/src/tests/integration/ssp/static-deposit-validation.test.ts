import { initTestingWallet } from "../../utils/spark-testing-wallet.js";
import { sha256 } from "@noble/hashes/sha2";
import { bytesToHex } from "@noble/hashes/utils";
import { retryUntilSuccess } from "../../utils/utils.js";

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

    const transactionId = await retryUntilSuccess(async () => {
      if (!signedTx) throw new Error("Tx not mined yet");
      return signedTx.id;
    });

    // Invalid transaction ID
    await expect(
      userWallet.getClaimStaticDepositQuote("invalid-txid", vout!),
    ).rejects.toThrow(/InvalidInputException/);

    // Valid transaction ID but not same as signedTx.id
    await expect(
      userWallet.getClaimStaticDepositQuote(
        bytesToHex(sha256("invalid-txid")),
        vout!,
      ),
    ).rejects.toThrow("Transaction not found");

    // Missing output index
    await expect(
      userWallet.getClaimStaticDepositQuote(transactionId, vout! + 10),
    ).rejects.toThrow(/Invalid(Operation|Input)Exception/);

    // Valid quote request for control
    // Wait for chain watcher to detect the deposit by polling for a valid quote
    const quote = await retryUntilSuccess(async () => {
      const q = await userWallet.getClaimStaticDepositQuote(
        transactionId,
        vout!,
      );
      if (!q) throw new Error("Quote not available yet");
      return q;
    });
    console.log(
      "Static deposit quote validation passed for correct parameters.",
    );

    // Invalid claim: missing signature
    await expect(
      userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote.creditAmountSats,
        outputIndex: vout!,
        sspSignature: "",
      }),
    ).rejects.toThrow(
      'Request ClaimStaticDeposit failed. [{"message":"Something went wrong."',
    );

    // Invalid claim: invalid credit amount
    await expect(
      userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote.creditAmountSats + 1000,
        outputIndex: vout!,
        sspSignature: quote.signature,
      }),
    ).rejects.toThrow(
      "The utxo amount is not enough to cover the claim amount",
    );

    // Invalid claim: wrong output index
    await expect(
      userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quote.creditAmountSats,
        outputIndex: vout! + 10,
        sspSignature: quote.signature,
      }),
    ).rejects.toThrow(/Invalid(Operation|Input)Exception/);
  }, 600000);
});
