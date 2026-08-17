import { describe, expect, it } from "@jest/globals";
import { hexToBytes } from "@noble/curves/utils";
import { decodeInvoice } from "../../../services/bolt11-spark.js";
import {
  manifestFeeSats,
  manifestGrossSats,
  manifestNetSatsFor,
} from "../../../utils/receive-quote.js";
import { SparkWalletTestingWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { waitForBalance, waitForClaim } from "../../utils/utils.js";

const DEPOSIT_AMOUNT = 10_000n;
const QUOTE_AMOUNT = 1_000;

async function fundedWallet() {
  const faucet = BitcoinFaucet.getInstance();
  const { wallet } = await SparkWalletTestingWithStream.initialize({
    options: { network: "LOCAL" },
  });

  const depositAddress = await wallet.getSingleUseDepositAddress();
  const signedTx = await faucet.sendToAddress(depositAddress, DEPOSIT_AMOUNT);
  await faucet.mineBlocksAndWaitForMiningToComplete(6);
  await wallet.claimDeposit(signedTx.id);
  await waitForBalance(wallet, DEPOSIT_AMOUNT);

  return wallet;
}

// TODO(SP-3812): re-enable — these sign the pre-attestor-envelope attestation format.
describe.skip("fee-quoted lightning receive", () => {
  it("settles an invoice issued against a signed quote", async () => {
    const payer = await fundedWallet();
    const { wallet: receiver } = await SparkWalletTestingWithStream.initialize({
      options: { network: "LOCAL" },
    });

    const quote = await receiver.getLightningReceiveQuote({
      amountSats: QUOTE_AMOUNT,
    });

    expect(quote.serializedManifest).toMatch(/^[0-9a-f]+$/);
    expect(quote.issuerSignature).toMatch(/^[0-9a-f]+$/);
    expect(quote.manifest.transferId).toBeTruthy();

    const receiverKey = hexToBytes(await receiver.getIdentityPublicKey());
    const grossSats = manifestGrossSats(quote.manifest);
    const netSats = manifestNetSatsFor(quote.manifest, receiverKey);

    // Unattributed, so the SSP quotes no markup and all three coincide. Pinned
    // because it is what makes the settlement assertions below unambiguous.
    expect(manifestFeeSats(quote.manifest)).toBe(0);
    expect(netSats).toBe(QUOTE_AMOUNT);
    expect(grossSats).toBe(QUOTE_AMOUNT);

    const request = await receiver.createLightningInvoice({
      amountSats: QUOTE_AMOUNT,
      memo: "quoted receive",
      quote,
    });

    // The SSP invoices the manifest's edge sum and refuses any other amount, so
    // the invoice is issued for the gross rather than for what was requested.
    expect(decodeInvoice(request.invoice.encodedInvoice).amountMSats).toBe(
      BigInt(grossSats) * 1000n,
    );

    const receiverClaimed = waitForClaim({ wallet: receiver });
    await payer.payLightningInvoice({
      invoice: request.invoice.encodedInvoice,
      maxFeeSats: 100,
    });
    await receiverClaimed;

    const { balance } = await receiver.getBalance();
    expect(balance).toBe(BigInt(netSats));

    // The settling transfer carries the manifest's own id. Only the v4 path can
    // do that — v3 lets the SSP mint the id — so this is what distinguishes a
    // committed quote from an ordinary receive that happens to pay the same
    // amount, which every other assertion here would accept.
    const settled = await receiver.getLightningReceiveRequest(request.id);
    expect(settled?.transfer?.sparkId).toBe(quote.manifest.transferId);
  }, 180_000);

  it("refuses to issue a second invoice for the same quote", async () => {
    const { wallet: receiver } = await SparkWalletTestingWithStream.initialize({
      options: { network: "LOCAL" },
    });

    const quote = await receiver.getLightningReceiveQuote({
      amountSats: QUOTE_AMOUNT,
    });

    await receiver.createLightningInvoice({
      amountSats: QUOTE_AMOUNT,
      quote,
    });

    // Matched on the SSP's own refusal rather than any rejection: a bare throw
    // is also satisfied by a transport, auth or expiry failure, which would pass
    // whether or not single use is enforced at all.
    await expect(
      receiver.createLightningInvoice({ amountSats: QUOTE_AMOUNT, quote }),
    ).rejects.toThrow(/already been committed/);
  }, 120_000);
});
