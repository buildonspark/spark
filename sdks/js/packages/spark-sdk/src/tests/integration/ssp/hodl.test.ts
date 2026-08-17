import { describe, expect, it, jest } from "@jest/globals";
import { bytesToHex } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { decodeInvoice } from "../../../services/bolt11-spark.js";
import {
  type LightningSendRequest,
  LightningReceiveRequestStatus,
  LightningSendRequestStatus,
} from "../../../types/index.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import {
  retryUntilSuccess,
  waitForBalance,
  waitForClaim,
} from "../../utils/utils.js";

const DEPOSIT_AMOUNT = 10000n;
const INVOICE_AMOUNT = 1000;

jest.retryTimes(0);

const PENDING_SEND_STATUSES = [
  LightningSendRequestStatus.CREATED,
  LightningSendRequestStatus.REQUEST_VALIDATED,
  LightningSendRequestStatus.LIGHTNING_PAYMENT_INITIATED,
];

describe("HODL lightning invoice", () => {
  it("pays a HODL invoice created by another wallet and settles on preimage reveal", async () => {
    const faucet = BitcoinFaucet.getInstance();

    const { wallet: aliceWallet } =
      await SparkWalletTestingWithStream.initialize({
        options: { network: "LOCAL" },
      });
    const { wallet: bobWallet } = await SparkWalletTestingWithStream.initialize(
      {
        options: { network: "LOCAL" },
      },
    );

    // Fund Alice so she has leaves to pay with.
    const depositAddress = await aliceWallet.getSingleUseDepositAddress();
    const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
    await faucet.mineBlocksAndWaitForMiningToComplete(6);
    await aliceWallet.claimDeposit(signedTx.id);
    await waitForBalance(aliceWallet, DEPOSIT_AMOUNT);

    // Bob keeps the preimage local — it is never shared with the SOs, which is
    // what makes this a HODL invoice on Spark.
    const preimage = crypto.getRandomValues(new Uint8Array(32));
    const wrongPreimage = new Uint8Array(preimage);
    wrongPreimage.set([wrongPreimage[0]! ^ 1], 0);
    const paymentHash = bytesToHex(sha256(preimage));

    const invoice = await bobWallet.createLightningHodlInvoice({
      amountSats: INVOICE_AMOUNT,
      paymentHash,
      memo: "hodl invoice test",
    });
    expect(invoice.status).toEqual(
      LightningReceiveRequestStatus.INVOICE_CREATED,
    );
    const decodedInvoice = decodeInvoice(invoice.invoice.encodedInvoice);
    expect(decodedInvoice.paymentHash).toEqual(paymentHash);

    const payResult = await aliceWallet.payLightningInvoice({
      invoice: invoice.invoice.encodedInvoice,
      maxFeeSats: 100,
    });
    expect("transferDirection" in payResult).toBe(false);
    const sendRequest = payResult as LightningSendRequest;
    expect(sendRequest.id).toBeDefined();

    // The payment must NOT settle while Bob withholds the preimage: neither
    // the SSP nor the SOs can complete it. A terminal status here means the
    // HODL flow is broken (e.g. a payment path assuming SOs hold the preimage).
    const pendingSend = await aliceWallet.getLightningSendRequest(
      sendRequest.id,
    );
    expect(PENDING_SEND_STATUSES).toContain(pendingSend?.status);

    const { balance: bobBalanceBeforeClaim } = await bobWallet.getBalance();
    expect(bobBalanceBeforeClaim).toBe(0n);

    // Register the claim listener before revealing the preimage so we don't
    // miss the event.
    // Timeout must cover the full claimHTLC retry budget below (30 x 2s) or a
    // slow SSP turns a successful settlement into a false "claim timeout".
    const bobClaimed = waitForClaim({
      wallet: bobWallet,
      throwOnTimeout: true,
      timeoutMs: 90_000,
    });

    await expect(
      bobWallet.claimHTLC(bytesToHex(wrongPreimage)),
    ).rejects.toThrow("preimage request not found");

    // Bob settles by revealing the preimage to the SO coordinator. This is
    // NOT_FOUND until the SSP creates the hash-locked receive-side transfer,
    // so retrying doubles as the readiness wait. Don't gate on receive-request
    // status: the SSP moves through statuses the SDK enum doesn't cover.
    await retryUntilSuccess(() => bobWallet.claimHTLC(bytesToHex(preimage)), {
      maxAttempts: 30,
      delayMs: 2000,
      timeoutMs: 60_000,
    });

    await bobClaimed;
    const { balance: bobBalance } = await bobWallet.getBalance();
    expect(bobBalance).toBe(BigInt(INVOICE_AMOUNT));

    const [completed, completedReceive] = await Promise.all([
      retryUntilSuccess(
        async () => {
          const req = await aliceWallet.getLightningSendRequest(sendRequest.id);
          if (req?.status !== LightningSendRequestStatus.TRANSFER_COMPLETED) {
            throw new Error(
              `send request ${sendRequest.id} not complete yet: ${req?.status}`,
            );
          }
          return req;
        },
        { maxAttempts: 30, delayMs: 2000, timeoutMs: 60_000 },
      ),
      retryUntilSuccess(
        async () => {
          const req = await bobWallet.getLightningReceiveRequest(invoice.id);
          if (
            req?.status !== LightningReceiveRequestStatus.TRANSFER_COMPLETED
          ) {
            throw new Error(
              `receive request ${invoice.id} not complete yet: ${req?.status}`,
            );
          }
          return req;
        },
        { maxAttempts: 30, delayMs: 2000, timeoutMs: 60_000 },
      ),
    ]);
    expect(completed.status).toEqual(
      LightningSendRequestStatus.TRANSFER_COMPLETED,
    );
    expect(completedReceive.status).toEqual(
      LightningReceiveRequestStatus.TRANSFER_COMPLETED,
    );

    // Sender was debited the payment amount (plus the SSP fee).
    const { balance: aliceBalance } = await aliceWallet.getBalance();
    expect(aliceBalance).toBeLessThan(DEPOSIT_AMOUNT - BigInt(INVOICE_AMOUNT));
  }, 180_000);
});
