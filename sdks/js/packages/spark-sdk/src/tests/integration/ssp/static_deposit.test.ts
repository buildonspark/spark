import {
  SparkWalletTestingWithStream,
  initTestingWallet,
} from "../../utils/spark-testing-wallet.js";
import { bytesToHex } from "@noble/hashes/utils";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { retryUntilSuccess, waitForBalance } from "../../utils/utils.js";

export const DEPOSIT_AMOUNT = 10000n;
const SECOND_DEPOSIT_AMOUNT = 20000n;
const THIRD_DEPOSIT_AMOUNT = 30000n;

describe("SSP static deposit address integration", () => {
  describe("Happy path testing", () => {
    it("should claim deposits to a static deposit address", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

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
        vout,
      );

      const quoteAmount = quote.creditAmountSats;
      const sspSignature = quote.signature;

      await retryUntilSuccess(
        async () =>
          await userWallet.claimStaticDeposit({
            transactionId,
            creditAmountSats: quoteAmount,
            sspSignature,
            outputIndex: vout!,
          }),
      );

      await waitForBalance(userWallet, BigInt(quoteAmount));

      // Test depositing money to the same address and second time and claiming.
      const signedTx2 = await faucet.sendToAddress(
        depositAddress,
        SECOND_DEPOSIT_AMOUNT,
      );
      const transactionId2 = signedTx2.id;
      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);
      // Test claiming and getting the quote without passing in the output index.
      const quote2 =
        await userWallet.getClaimStaticDepositQuote(transactionId2);
      const quoteAmount2 = quote2.creditAmountSats;
      const sspSignature2 = quote2.signature;
      await userWallet.claimStaticDeposit({
        transactionId: transactionId2,
        creditAmountSats: quoteAmount2,
        sspSignature: sspSignature2,
      });
      await waitForBalance(userWallet, BigInt(quoteAmount + quoteAmount2));

      // Test depositing money to the same address and test claim with max fee flow.
      const signedTx3 = await faucet.sendToAddress(
        depositAddress,
        THIRD_DEPOSIT_AMOUNT,
      );
      const transactionId3 = signedTx3.id;
      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);
      // Get quote so we can calculate the expected balance. Not needed for actual flow.
      const quote3 =
        await userWallet.getClaimStaticDepositQuote(transactionId3);
      const quoteAmount3 = quote3.creditAmountSats;
      await userWallet.claimStaticDepositWithMaxFee({
        transactionId: transactionId3,
        maxFee: 1000,
      });
      await waitForBalance(
        userWallet,
        BigInt(quoteAmount + quoteAmount2 + quoteAmount3),
      );
      // Get transfers should include static deposit transfers.
      const transfers = await userWallet.getTransfers();
      expect(transfers.transfers.length).toBe(3);
    }, 60000);

    it("should return a valid transfer ID that can be looked up via SSP", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocks(6);

      const transactionId = signedTx.id;
      let vout: number | undefined;
      for (let i = 0; i < signedTx.outputsLength; i++) {
        const output = signedTx.getOutput(i);
        if (output.amount === DEPOSIT_AMOUNT) {
          vout = i;
          break;
        }
      }

      const quote = await userWallet.getClaimStaticDepositQuote(
        transactionId,
        vout,
      );
      const quoteAmount = quote.creditAmountSats;
      const sspSignature = quote.signature;

      // Claim the deposit and capture the returned transfer ID
      const claimResult = await retryUntilSuccess(
        async () =>
          await userWallet.claimStaticDeposit({
            transactionId,
            creditAmountSats: quoteAmount,
            sspSignature,
            outputIndex: vout!,
          }),
      );

      // The transfer ID must be defined — this is the Spark transfer ID
      // that clients use to track the deposit claim.
      // Regression: PR #24467 broke this by returning the SSP internal
      // transfer_id instead of transfer.external_spark_id.
      expect(claimResult).toBeDefined();
      expect(claimResult!.transferId).toBeDefined();
      expect(claimResult!.transferId.length).toBeGreaterThan(0);

      await waitForBalance(userWallet, BigInt(quoteAmount));

      // Look up the transfer by the returned ID via the SSP.
      // If the wrong ID was returned, this lookup will fail.
      const transfer = await userWallet.getTransferFromSsp(
        claimResult!.transferId,
      );
      expect(transfer).toBeDefined();
      expect(transfer!.userRequest).toBeDefined();
      expect((transfer!.userRequest as any).typename).toBe(
        "ClaimStaticDeposit",
      );
    }, 60000);

    it("should create a refund transaction", async () => {
      const faucet = BitcoinFaucet.getInstance();

      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      expect(signedTx).toBeDefined();

      const transactionId = signedTx.id;

      let vout: number | undefined;

      for (let i = 0; i < signedTx.outputsLength; i++) {
        const output = signedTx.getOutput(i);
        if (output.amount === DEPOSIT_AMOUNT) {
          vout = i;
          break;
        }
      }

      const refundAddress = await faucet.getNewAddress();

      // Chainwatcher needs to catch up. Could take a few seconds so retry until success.
      const refundTx = await retryUntilSuccess(
        async () =>
          await userWallet.refundStaticDeposit({
            depositTransactionId: transactionId,
            destinationAddress: refundAddress,
            satsPerVbyteFee: 2,
          }),
      );

      expect(refundTx).toBeDefined();

      // Calling it again should create a new transaction.
      const refundTx2 = await userWallet.refundStaticDeposit({
        depositTransactionId: transactionId,
        destinationAddress: refundAddress,
        outputIndex: vout!,
        satsPerVbyteFee: 2,
      });

      expect(refundTx2).toBeDefined();

      expect(refundTx).not.toBe(refundTx2);
    }, 60000);

    it("should return the right amount of txns when querying for utxos sent to a static deposit address", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();
      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

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
        vout,
      );

      const quoteAmount = quote.creditAmountSats;
      const sspSignature = quote.signature;

      await retryUntilSuccess(
        async () =>
          await userWallet.claimStaticDeposit({
            transactionId,
            creditAmountSats: quoteAmount,
            sspSignature,
            outputIndex: vout!,
          }),
      );

      await waitForBalance(userWallet, BigInt(quoteAmount));

      // Test depositing money to the same address and second time and claiming.
      const signedTx2 = await faucet.sendToAddress(
        depositAddress,
        SECOND_DEPOSIT_AMOUNT,
      );
      const transactionId2 = signedTx2.id;
      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);
      // Test claiming and getting the quote without passing in the output index.
      const quote2 =
        await userWallet.getClaimStaticDepositQuote(transactionId2);
      const quoteAmount2 = quote2.creditAmountSats;
      const sspSignature2 = quote2.signature;
      await retryUntilSuccess(
        async () =>
          await userWallet.claimStaticDeposit({
            transactionId: transactionId2,
            creditAmountSats: quoteAmount2,
            sspSignature: sspSignature2,
          }),
      );
      await waitForBalance(userWallet, BigInt(quoteAmount + quoteAmount2));

      // Test depositing money to the same address and test claim with max fee flow.
      const signedTx3 = await faucet.sendToAddress(
        depositAddress,
        THIRD_DEPOSIT_AMOUNT,
      );
      const transactionId3 = signedTx3.id;
      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);
      // Get quote so we can calculate the expected balance. Not needed for actual flow.
      const quote3 =
        await userWallet.getClaimStaticDepositQuote(transactionId3);
      const quoteAmount3 = quote3.creditAmountSats;
      await retryUntilSuccess(
        async () =>
          await userWallet.claimStaticDepositWithMaxFee({
            transactionId: transactionId3,
            maxFee: 1000,
          }),
      );
      await waitForBalance(
        userWallet,
        BigInt(quoteAmount + quoteAmount2 + quoteAmount3),
      );
      // Get transfers should include static deposit transfers.
      const transfers = await userWallet.getTransfers();
      expect(transfers.transfers.length).toBe(3);

      for (let i = 0; i < 98; i++) {
        await faucet.sendToAddress(depositAddress, THIRD_DEPOSIT_AMOUNT);
      }

      await faucet.mineBlocks(6);

      let utxosExcludeClaimed: any[] = [];
      const utxosExcludeClaimedExpected = 98;
      const maxAttempts = 10;
      for (let attempt = 1; attempt <= 10; attempt++) {
        utxosExcludeClaimed = await userWallet.getUtxosForDepositAddress(
          depositAddress,
          100,
          0,
          true,
        );
        if (utxosExcludeClaimed.length === utxosExcludeClaimedExpected) break;
        if (attempt < maxAttempts)
          await new Promise((r) => setTimeout(r, 5000));
      }

      expect(utxosExcludeClaimed.length).toBe(98);

      const utxos = await userWallet.getUtxosForDepositAddress(
        depositAddress,
        100,
        0,
        false,
      );
      expect(utxos.length).toBe(100);
    }, 60000);
  });

  describe("Concurrency testing", () => {
    it("Wallet balance should be correct after concurrent claims of the same wallet initted in different places", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: aliceWallet, mnemonic: aliceMnemonic } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const { wallet: aliceWallet2, mnemonic: aliceMnemonic2 } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
          mnemonicOrSeed: aliceMnemonic,
        });

      expect(aliceMnemonic).toEqual(aliceMnemonic2);

      const depositAddress = await aliceWallet.getStaticDepositAddress();
      const depositAddress2 = await aliceWallet2.getStaticDepositAddress();
      expect(depositAddress).toEqual(depositAddress2);

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      await faucet.mineBlocks(6);

      const [quoteResult, quote2Result] = await Promise.allSettled([
        aliceWallet.getClaimStaticDepositQuote(signedTx.id),
        aliceWallet2.getClaimStaticDepositQuote(signedTx.id),
      ]);

      // Extract the actual quote values
      const quote =
        quoteResult.status === "fulfilled" ? quoteResult.value : null;
      const quote2 =
        quote2Result.status === "fulfilled" ? quote2Result.value : null;

      expect(JSON.stringify(quote)).toEqual(JSON.stringify(quote2));

      const claims = await Promise.allSettled([
        aliceWallet.claimStaticDeposit({
          transactionId: signedTx.id,
          creditAmountSats: quote!.creditAmountSats,
          sspSignature: quote!.signature,
        }),
        aliceWallet2.claimStaticDeposit({
          transactionId: signedTx.id,
          creditAmountSats: quote2!.creditAmountSats,
          sspSignature: quote2!.signature,
        }),
      ]);

      const successes = claims.filter(
        (result) => result.status === "fulfilled",
      );
      const failures = claims.filter((result) => result.status === "rejected");

      expect(successes).toHaveLength(1);
      expect(failures).toHaveLength(1);

      await waitForBalance(aliceWallet, BigInt(quote!.creditAmountSats));
      await waitForBalance(aliceWallet2, BigInt(quote2!.creditAmountSats));
    }, 60000);

    it("Wallet balance should be correct after concurrent claims of the same initted wallet", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await aliceWallet.getStaticDepositAddress();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      await faucet.mineBlocks(6);

      const [quoteResult, quote2Result] = await Promise.allSettled([
        aliceWallet.getClaimStaticDepositQuote(signedTx.id),
        aliceWallet.getClaimStaticDepositQuote(signedTx.id),
      ]);

      // Extract the actual quote values
      const quote =
        quoteResult.status === "fulfilled" ? quoteResult.value : null;
      const quote2 =
        quote2Result.status === "fulfilled" ? quote2Result.value : null;

      expect(JSON.stringify(quote)).toEqual(JSON.stringify(quote2));

      const concurrentCalls = 5; // Number of simultaneous calls

      const promises = Array.from({ length: concurrentCalls }, () =>
        aliceWallet.claimStaticDeposit({
          transactionId: signedTx.id,
          creditAmountSats: quote!.creditAmountSats,
          sspSignature: quote!.signature,
        }),
      );

      const claims = await Promise.allSettled(promises);

      const successes = claims.filter(
        (result) => result.status === "fulfilled",
      );
      const failures = claims.filter((result) => result.status === "rejected");

      expect(successes).toHaveLength(1);
      expect(failures).toHaveLength(4);

      await waitForBalance(aliceWallet, BigInt(quote!.creditAmountSats));
    }, 60000);
  });

  describe("Quote unhappy path testing", () => {
    it("should error claim quote from a different wallet", async () => {
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

      const faucet = BitcoinFaucet.getInstance();

      const depositAddress = await aliceWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      expect(signedTx).toBeDefined();

      const transactionId = signedTx.id;

      await expect(
        bobWallet.getClaimStaticDepositQuote(transactionId),
      ).rejects.toThrow();
    }, 60000);

    it("should error if txid does not exist", async () => {
      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const faucet = BitcoinFaucet.getInstance();

      const depositAddress = await aliceWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      expect(signedTx).toBeDefined();

      const transactionId = signedTx.id;

      await expect(
        aliceWallet.getClaimStaticDepositQuote(
          `${transactionId.slice(0, -6)}abcdef`,
        ),
      ).rejects.toThrow();
    });

    it("should error claim quote if tx already claimed", async () => {
      const { wallet: aliceWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const faucet = BitcoinFaucet.getInstance();

      const depositAddress = await aliceWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();

      const signedTx = await faucet.sendToAddress(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      expect(signedTx).toBeDefined();

      const transactionId = signedTx.id;

      const quote = await aliceWallet.getClaimStaticDepositQuote(transactionId);

      await aliceWallet.claimStaticDeposit({
        transactionId: transactionId,
        creditAmountSats: quote.creditAmountSats,
        sspSignature: quote.signature,
      });
      await waitForBalance(aliceWallet, BigInt(quote.creditAmountSats));

      await expect(
        aliceWallet.getClaimStaticDepositQuote(transactionId),
      ).rejects.toThrow();
    }, 60000);
  });

  describe("Claim unhappy path testing", () => {
    it("should reject claim with fake SSP signature", async () => {
      console.log("Initializing wallet for fake SSP signature test...");
      const {
        wallet: userWallet,
        signedTx,
        vout,
        faucet,
      } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      const transactionId = signedTx.id;

      console.log("Fetching claim quote for static deposit...");
      const quote = await userWallet.getClaimStaticDepositQuote(
        transactionId,
        vout,
      );

      const quoteAmount = quote.creditAmountSats;

      // Generate a fake signature (64 bytes of random data to simulate a signature)
      const fakeSignature = new Uint8Array(64);
      crypto.getRandomValues(fakeSignature);
      console.log("Expecting error when claiming with fake signature...");
      await expect(
        userWallet.claimStaticDeposit({
          transactionId,
          creditAmountSats: quoteAmount,
          sspSignature: bytesToHex(fakeSignature),
          outputIndex: vout!,
        }),
      ).rejects.toThrow(
        'Request ClaimStaticDeposit failed. [{"message":"Something went wrong."',
      );
    }, 600000);

    it("should reject claiming the same deposit twice", async () => {
      console.log("Initializing wallet for double-claim test...");
      const {
        wallet: userWallet,
        signedTx,
        vout,
        faucet,
      } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      const transactionId = signedTx.id;

      console.log("Fetching claim quote for static deposit...");
      const quote = await userWallet.getClaimStaticDepositQuote(
        transactionId,
        vout,
      );

      const quoteAmount = quote.creditAmountSats;
      const sspSignature = quote.signature;

      console.log("Attempting to claim static deposit for the first time...");
      const outputs = await userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quoteAmount,
        sspSignature,
        outputIndex: vout!,
      });

      expect(outputs).toBeDefined();

      console.log(
        "Expecting error when attempting to claim the same deposit twice...",
      );
      await expect(
        userWallet.claimStaticDeposit({
          transactionId,
          creditAmountSats: quoteAmount,
          sspSignature,
          outputIndex: vout!,
        }),
      ).rejects.toThrow(/Invalid(Operation|Input)Exception/);
    }, 600000);

    it("Claim, then try to refund.", async () => {
      console.log("Initializing wallet for claim and refund test...");
      const {
        wallet: userWallet,
        depositAddress,
        signedTx,
        vout,
        faucet,
      } = await initTestingWallet(DEPOSIT_AMOUNT, "LOCAL");

      // Wait for the transaction to be mined
      await faucet.mineBlocks(6);

      expect(signedTx).toBeDefined();

      const transactionId = signedTx.id;

      const quote = await userWallet.getClaimStaticDepositQuote(
        transactionId,
        vout,
      );

      const quoteAmount = quote.creditAmountSats;
      const sspSignature = quote.signature;

      console.log("Attempting to claim static deposit...");
      await userWallet.claimStaticDeposit({
        transactionId,
        creditAmountSats: quoteAmount,
        sspSignature,
        outputIndex: vout!,
      });

      await waitForBalance(userWallet, BigInt(quoteAmount));

      const sparkAddress = await userWallet.getSparkAddress();
      const transfer = await userWallet.transfer({
        amountSats: quoteAmount,
        receiverSparkAddress: sparkAddress,
      });
      expect(transfer).toBeDefined();

      // Try to refund the deposit after claiming and transfer
      console.log("Attempting refund of claimed deposit...");
      await expect(
        userWallet.refundStaticDeposit({
          depositTransactionId: transactionId,
          destinationAddress: depositAddress,
          fee: 301,
        }),
      ).rejects.toThrow();
    }, 600000);
  });
});
