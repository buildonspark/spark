import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { waitForBalance } from "../../utils/utils.js";
import { CurrencyUnit } from "@lightsparkdev/core";
import { Transaction } from "@scure/btc-signer";

const DEPOSIT_AMOUNT = 10000n;

function findOutputVout(tx: Transaction, amount: bigint): number | undefined {
  for (let i = 0; i < tx.outputsLength; i++) {
    const output = tx.getOutput(i);
    if (output.amount === amount) {
      return i;
    }
  }
  return undefined;
}

describe("SSP instant static deposit integration", () => {
  describe("Quote flow", () => {
    it("should get an instant static deposit quote", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      expect(depositAddress).toBeDefined();

      const { tx: signedTx } = await faucet.sendToAddressRbf(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocks(1);
      expect(signedTx).toBeDefined();

      const transactionId = signedTx.id;
      const vout = findOutputVout(signedTx, DEPOSIT_AMOUNT);
      expect(vout).toBeDefined();

      const quoteResult =
        await userWallet.experimental_GetInstantStaticDepositQuote(
          transactionId,
          vout!,
        );

      expect(quoteResult).toBeDefined();
      expect(quoteResult.quote).toBeDefined();
      expect(quoteResult.quote.transactionId).toBe(transactionId);
      expect(quoteResult.quote.outputIndex).toBe(vout!);
      expect(quoteResult.quote.depositAmount).toBeDefined();
      expect(quoteResult.quote.depositAmount.originalUnit).toBe(
        CurrencyUnit.SATOSHI,
      );
      expect(quoteResult.quote.creditAmount).toBeDefined();
      expect(quoteResult.quote.quoteSignature).toBeDefined();
      expect(quoteResult.quote.quoteSignature.length).toBeGreaterThan(0);

      expect(quoteResult.fulfillmentPlans).toBeDefined();
      expect(quoteResult.fulfillmentPlans.length).toBeGreaterThan(0);

      const plan = quoteResult.fulfillmentPlans[0]!;
      expect(plan.amount).toBeDefined();
      expect(typeof plan.confirmations).toBe("number");
      expect(plan.status).toBeDefined();

      await userWallet.cleanupConnections();
    }, 60000);
  });

  describe("Claim flow", () => {
    it("should claim an instant static deposit", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      const { tx: signedTx } = await faucet.sendToAddressRbf(
        depositAddress,
        DEPOSIT_AMOUNT,
      );
      await faucet.mineBlocks(1);

      const transactionId = signedTx.id;
      const vout = findOutputVout(signedTx, DEPOSIT_AMOUNT);

      const quoteResult =
        await userWallet.experimental_GetInstantStaticDepositQuote(
          transactionId,
          vout!,
          "test-partner",
        );

      const claimResult =
        await userWallet.experimental_ClaimInstantStaticDeposit({
          quote: quoteResult.quote,
          plan: quoteResult.fulfillmentPlans[0]!,
          transactionId: transactionId,
          outputIndex: vout!,
        });

      expect(claimResult).toBeDefined();
      expect(claimResult.claimId).toBeDefined();

      await waitForBalance(
        userWallet,
        BigInt(quoteResult.quote.creditAmount.originalValue),
      );

      await userWallet.cleanupConnections();
    }, 60000);

    it("should claim an instant static deposit happy path mine 1 block after quote", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: {
            network: "LOCAL",
          },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      const { tx: signedTx } = await faucet.sendToAddressRbf(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      const transactionId = signedTx.id;
      const vout = findOutputVout(signedTx, DEPOSIT_AMOUNT);

      const quoteResult =
        await userWallet.experimental_GetInstantStaticDepositQuote(
          transactionId,
          vout!,
          "test-partner",
        );

      await faucet.mineBlocksAndWaitForMiningToComplete(1);

      const claimResult =
        await userWallet.experimental_ClaimInstantStaticDeposit({
          quote: quoteResult.quote,
          plan: quoteResult.fulfillmentPlans[0]!,
          transactionId: transactionId,
          outputIndex: vout!,
        });

      expect(claimResult).toBeDefined();
      expect(claimResult.claimId).toBeDefined();

      await waitForBalance(
        userWallet,
        BigInt(quoteResult.quote.creditAmount.originalValue),
      );

      await userWallet.cleanupConnections();
    }, 60000);

    it("should claim after RBF with same amount before mining", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const MAX_ATTEMPTS = 3;

      for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
        const { wallet: userWallet } =
          await SparkWalletTestingWithStream.initialize({
            options: { network: "LOCAL" },
          });

        const depositAddress = await userWallet.getStaticDepositAddress();
        const { tx: tx1, coin } = await faucet.sendToAddressRbf(
          depositAddress,
          DEPOSIT_AMOUNT,
        );

        const vout1 = findOutputVout(tx1, DEPOSIT_AMOUNT);
        expect(vout1).toBeDefined();

        // Get quote while tx1 is unconfirmed
        const quoteResult =
          await userWallet.experimental_GetInstantStaticDepositQuote(
            tx1.id,
            vout1!,
            "test-partner",
          );

        // Claim while tx1 is still unconfirmed
        const claimResult =
          await userWallet.experimental_ClaimInstantStaticDeposit({
            quote: quoteResult.quote,
            plan: quoteResult.fulfillmentPlans[0]!,
            transactionId: tx1.id,
            outputIndex: vout1!,
          });

        expect(claimResult).toBeDefined();
        expect(claimResult.claimId).toBeDefined();

        // RBF tx1 with same amount before mining
        let tx2: Transaction;
        try {
          tx2 = await faucet.replaceTransaction(
            coin,
            depositAddress,
            DEPOSIT_AMOUNT,
          );
        } catch (err) {
          const isMissingInput =
            err instanceof Error &&
            err.message.includes("bad-txns-inputs-missingorspent");
          if (isMissingInput) {
            console.warn(
              `Attempt ${attempt}/${MAX_ATTEMPTS}: tx1 was already confirmed, retrying...`,
            );
            await userWallet.cleanupConnections();
            if (attempt === MAX_ATTEMPTS) {
              console.warn(
                "All attempts hit automine, cannot test RBF — skipping",
              );
              return;
            }
            continue;
          }
          throw err;
        }

        // Now mine to confirm the replacement tx
        await faucet.mineBlocks(1);

        await waitForBalance(
          userWallet,
          BigInt(quoteResult.quote.creditAmount.originalValue),
        );

        await userWallet.cleanupConnections();
        return;
      }
    }, 120000);
  });

  describe("1-conf claim flow", () => {
    it("should claim 1-conf plan with confirmed transaction", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      const { tx } = await faucet.sendToAddressRbf(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      const transactionId = tx.id;
      const vout = findOutputVout(tx, DEPOSIT_AMOUNT);
      expect(vout).toBeDefined();

      const quoteResult =
        await userWallet.experimental_GetInstantStaticDepositQuote(
          transactionId,
          vout!,
        );

      const oneConfPlan = quoteResult.fulfillmentPlans.find(
        (p) => p.confirmations === 1,
      );
      expect(oneConfPlan).toBeDefined();

      await faucet.mineBlocks(1);

      const claimResult =
        await userWallet.experimental_ClaimInstantStaticDeposit({
          quote: quoteResult.quote,
          plan: oneConfPlan!,
          transactionId,
          outputIndex: vout!,
        });

      expect(claimResult).toBeDefined();
      expect(claimResult.claimId).toBeDefined();

      await waitForBalance(
        userWallet,
        BigInt(quoteResult.quote.creditAmount.originalValue),
      );

      await userWallet.cleanupConnections();
    }, 60000);

    it("should succeed to claim after RBF replacement with same amount", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      const { tx: tx1, coin } = await faucet.sendToAddressRbf(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      const vout1 = findOutputVout(tx1, DEPOSIT_AMOUNT);
      expect(vout1).toBeDefined();

      const quoteResult =
        await userWallet.experimental_GetInstantStaticDepositQuote(
          tx1.id,
          vout1!,
        );

      const oneConfPlan = quoteResult.fulfillmentPlans.find(
        (p) => p.confirmations === 1,
      );
      expect(oneConfPlan).toBeDefined();

      // Replace with same amount but higher fee — creates new txid
      let tx2: Transaction;
      try {
        tx2 = await faucet.replaceTransaction(
          coin,
          depositAddress,
          DEPOSIT_AMOUNT,
        );
      } catch (err) {
        // tx1 was already confirmed by auto-miner — can't test RBF
        const isMissingInput =
          err instanceof Error &&
          err.message.includes("bad-txns-inputs-missingorspent");
        if (isMissingInput) {
          console.warn(
            "tx1 was already confirmed, cannot test RBF replacement — skipping",
          );
          await userWallet.cleanupConnections();
          return;
        }
        throw err;
      }

      await faucet.mineBlocks(1);

      const vout2 = findOutputVout(tx2, DEPOSIT_AMOUNT);
      expect(vout2).toBeDefined();

      // 1-conf plan with same amount should succeed even after RBF,
      // because the replacement tx pays the same address and amount.
      const claimResult =
        await userWallet.experimental_ClaimInstantStaticDeposit({
          quote: quoteResult.quote,
          plan: oneConfPlan!,
          transactionId: tx2.id,
          outputIndex: vout2!,
        });

      expect(claimResult).toBeDefined();
      expect(claimResult.claimId).toBeDefined();

      await waitForBalance(
        userWallet,
        BigInt(quoteResult.quote.creditAmount.originalValue),
      );

      await userWallet.cleanupConnections();
    }, 60000);

    it("should fail to claim after RBF replacement with different amount", async () => {
      const faucet = BitcoinFaucet.getInstance();
      const { wallet: userWallet } =
        await SparkWalletTestingWithStream.initialize({
          options: { network: "LOCAL" },
        });

      const depositAddress = await userWallet.getStaticDepositAddress();
      const { tx: tx1, coin } = await faucet.sendToAddressRbf(
        depositAddress,
        DEPOSIT_AMOUNT,
      );

      const vout1 = findOutputVout(tx1, DEPOSIT_AMOUNT);
      expect(vout1).toBeDefined();

      const quoteResult =
        await userWallet.experimental_GetInstantStaticDepositQuote(
          tx1.id,
          vout1!,
        );

      const oneConfPlan = quoteResult.fulfillmentPlans.find(
        (p) => p.confirmations === 1,
      );
      expect(oneConfPlan).toBeDefined();

      // Replace with different (smaller) amount
      const differentAmount = DEPOSIT_AMOUNT / 2n;
      let tx2: Transaction;
      try {
        tx2 = await faucet.replaceTransaction(
          coin,
          depositAddress,
          differentAmount,
        );
      } catch (err) {
        // tx1 was already confirmed by auto-miner — can't test RBF
        const isMissingInput =
          err instanceof Error &&
          err.message.includes("bad-txns-inputs-missingorspent");
        if (isMissingInput) {
          console.warn(
            "tx1 was already confirmed, cannot test RBF replacement — skipping",
          );
          await userWallet.cleanupConnections();
          return;
        }
        throw err;
      }

      await faucet.mineBlocks(1);

      const vout2 = findOutputVout(tx2, differentAmount);
      expect(vout2).toBeDefined();

      // Claim with original quote (for DEPOSIT_AMOUNT) but RBF'd tx (for differentAmount)
      await expect(
        userWallet.experimental_ClaimInstantStaticDeposit({
          quote: quoteResult.quote,
          plan: oneConfPlan!,
          transactionId: tx2.id,
          outputIndex: vout2!,
        }),
      ).rejects.toThrow();

      await userWallet.cleanupConnections();
    }, 60000);
  });

  describe("0-conf RBF", () => {
    const MAX_RBF_ATTEMPTS = 3;

    // TODO: Fix 0-conf RBF claim — SSP returns "UTXO is spent or not found" for unconfirmed replacement txs
    it.skip("should claim 0-conf plan after RBF with same amount", async () => {
      const faucet = BitcoinFaucet.getInstance();

      for (let attempt = 1; attempt <= MAX_RBF_ATTEMPTS; attempt++) {
        const { wallet: userWallet } =
          await SparkWalletTestingWithStream.initialize({
            options: { network: "LOCAL" },
          });

        const depositAddress = await userWallet.getStaticDepositAddress();
        const { tx: tx1, coin } = await faucet.sendToAddressRbf(
          depositAddress,
          DEPOSIT_AMOUNT,
        );

        const vout1 = findOutputVout(tx1, DEPOSIT_AMOUNT);
        expect(vout1).toBeDefined();

        const quoteResult =
          await userWallet.experimental_GetInstantStaticDepositQuote(
            tx1.id,
            vout1!,
            "test-partner",
          );

        const zeroConfPlan = quoteResult.fulfillmentPlans.find(
          (p) => p.confirmations === 0,
        );
        expect(zeroConfPlan).toBeDefined();

        let tx2: Transaction;
        try {
          tx2 = await faucet.replaceTransaction(
            coin,
            depositAddress,
            DEPOSIT_AMOUNT,
          );
        } catch (err) {
          const isMissingInput =
            err instanceof Error &&
            err.message.includes("bad-txns-inputs-missingorspent");
          if (isMissingInput) {
            console.warn(
              `Attempt ${attempt}/${MAX_RBF_ATTEMPTS}: tx1 was already confirmed, retrying...`,
            );
            await userWallet.cleanupConnections();
            if (attempt === MAX_RBF_ATTEMPTS) {
              console.warn(
                "All attempts hit automine, cannot test 0-conf RBF — skipping",
              );
              return;
            }
            continue;
          }
          throw err;
        }

        const vout2 = findOutputVout(tx2, DEPOSIT_AMOUNT);
        expect(vout2).toBeDefined();

        const claimResult =
          await userWallet.experimental_ClaimInstantStaticDeposit({
            quote: quoteResult.quote,
            plan: zeroConfPlan!,
            transactionId: tx2.id,
            outputIndex: vout2!,
          });

        expect(claimResult).toBeDefined();
        expect(claimResult.claimId).toBeDefined();

        await waitForBalance(
          userWallet,
          BigInt(quoteResult.quote.creditAmount.originalValue),
        );

        await userWallet.cleanupConnections();
        return;
      }
    }, 120000);

    it("should fail to claim 0-conf plan after RBF with different amount", async () => {
      const faucet = BitcoinFaucet.getInstance();

      for (let attempt = 1; attempt <= MAX_RBF_ATTEMPTS; attempt++) {
        const { wallet: userWallet } =
          await SparkWalletTestingWithStream.initialize({
            options: { network: "LOCAL" },
          });

        const depositAddress = await userWallet.getStaticDepositAddress();
        const { tx: tx1, coin } = await faucet.sendToAddressRbf(
          depositAddress,
          DEPOSIT_AMOUNT,
        );

        const vout1 = findOutputVout(tx1, DEPOSIT_AMOUNT);
        expect(vout1).toBeDefined();

        const quoteResult =
          await userWallet.experimental_GetInstantStaticDepositQuote(
            tx1.id,
            vout1!,
            "test-partner",
          );

        const zeroConfPlan = quoteResult.fulfillmentPlans.find(
          (p) => p.confirmations === 0,
        );
        expect(zeroConfPlan).toBeDefined();

        const differentAmount = DEPOSIT_AMOUNT / 2n;
        let tx2: Transaction;
        try {
          tx2 = await faucet.replaceTransaction(
            coin,
            depositAddress,
            differentAmount,
          );
        } catch (err) {
          const isMissingInput =
            err instanceof Error &&
            err.message.includes("bad-txns-inputs-missingorspent");
          if (isMissingInput) {
            console.warn(
              `Attempt ${attempt}/${MAX_RBF_ATTEMPTS}: tx1 was already confirmed, retrying...`,
            );
            await userWallet.cleanupConnections();
            if (attempt === MAX_RBF_ATTEMPTS) {
              console.warn(
                "All attempts hit automine, cannot test 0-conf RBF — skipping",
              );
              return;
            }
            continue;
          }
          throw err;
        }

        const vout2 = findOutputVout(tx2, differentAmount);
        expect(vout2).toBeDefined();

        await expect(
          userWallet.experimental_ClaimInstantStaticDeposit({
            quote: quoteResult.quote,
            plan: zeroConfPlan!,
            transactionId: tx2.id,
            outputIndex: vout2!,
          }),
        ).rejects.toThrow();

        await userWallet.cleanupConnections();
        return;
      }
    }, 120000);
  });
});
