import { describe, expect, it, jest } from "@jest/globals";
import { hexToBytes } from "@noble/curves/utils";
import { uuidv7obj } from "uuidv7";
import { SparkValidationError } from "../errors/index.js";
import type { Transfer } from "../proto/spark.js";
import type { DecodedInvoice } from "../services/bolt11-spark.js";
import type { LeafKeyTweak } from "../services/transfer.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import { encodeSparkAddress } from "../utils/address.js";
import { Network } from "../utils/network.js";

const FALLBACK_INVOICE =
  "lnbc13u1p5xalmkpp5z79uwgne7znz76plf0q4zxmh8t3wke6gsnm5kn67h4satpgflkmssp5azht5ywc5s4m40jf9h0nwlr959a34n72pns50lfm93zz8lvs7nqsxq9z0rgqnp4q0p92sfan5vj2a4f8q3gsfsy8qp60maeuxz858c5x0hvt5u0p0h9jr9yqtqd37k2ya0pv8pqeyjs4lklcexjyw600g9qqp62r4j0ph8fcmlfwqqqqzfv7u6g85qqqqqqqqqqthqq9qpz9cat0ndmwmfx036y9fxfhdufta3mn95ta9xw34ynlwg7euxjck85ysq0gfqqqqq7u6egqrhxk2qqn3qqcqzpgdq2w3jhxap3xv9qyyssqfahd64hu0lffl7cw2e4evu400s09yeupypvnfjvjjyq8rh05y9gzd3dqnmkvuyd9jszyhmdey75dujz8xaufgahsxkqktf3wxny8ghsqpk4mg8";

const LIGHTNING_INVOICE =
  "lnbc2500u1pvjluezsp5zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zyg3zygspp5qqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqqqsyqcyq5rqwzqfqypqdq5xysxxatsyp3k7enxv4jsxqzpu9qrsgquk0rl77nj30yxdy8j9vdx85fkpmdla2087ne0xh8nhedh8w27kyke0lp53ut353s06fv3qfegext0eh0ymjpf39tuven09sam30g4vgpfna3rh";

function createFallbackWallet() {
  const tryPayOverSpark = jest.fn<
    (
      invoice: DecodedInvoice,
      amountSats: number,
      network: Network,
      transferId: string,
    ) => Promise<{ id: string }>
  >(() => Promise.resolve({ id: "transfer" }));
  const wallet = Object.create(SparkWallet.prototype) as unknown as {
    config: { getNetwork: () => Network };
    logger: { warn: () => void };
    payLightningInvoice: SparkWallet["payLightningInvoice"];
    tryPayOverSpark: typeof tryPayOverSpark;
  };
  wallet.config = { getNetwork: () => Network.MAINNET };
  wallet.logger = { warn: jest.fn() };
  wallet.tryPayOverSpark = tryPayOverSpark;
  return wallet;
}

describe("payLightningInvoice de-duplication", () => {
  it("passes the supplied transfer ID to the Spark fallback", async () => {
    const wallet = createFallbackWallet();
    const transferId = uuidv7obj();

    await wallet.payLightningInvoice({
      invoice: FALLBACK_INVOICE,
      maxFeeSats: 0,
      preferSpark: true,
      transferId,
    });

    expect(wallet.tryPayOverSpark).toHaveBeenCalledWith(
      expect.anything(),
      1300,
      Network.MAINNET,
      transferId.toString(),
    );
  });

  it("generates a UUIDv7 for the Spark fallback when omitted", async () => {
    const wallet = createFallbackWallet();

    await wallet.payLightningInvoice({
      invoice: FALLBACK_INVOICE,
      maxFeeSats: 0,
      preferSpark: true,
    });

    const transferId = wallet.tryPayOverSpark.mock.calls[0]![3];
    expect(transferId).toMatch(
      /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
    );
  });

  it("surfaces a retried payment's error instead of falling back to Lightning", async () => {
    const wallet = createFallbackWallet();
    const params = {
      invoice: FALLBACK_INVOICE,
      maxFeeSats: 0,
      preferSpark: true,
      transferId: uuidv7obj(),
    };
    const duplicate = new Error("transfer 1 already exists");

    await wallet.payLightningInvoice(params);
    wallet.tryPayOverSpark.mockRejectedValueOnce(duplicate);

    await expect(wallet.payLightningInvoice(params)).rejects.toBe(duplicate);
  });

  it("rejects runtime string IDs before trying a payment rail", async () => {
    const wallet = createFallbackWallet();

    await expect(
      wallet.payLightningInvoice({
        invoice: FALLBACK_INVOICE,
        maxFeeSats: 0,
        preferSpark: true,
        transferId: "not-a-uuid" as never,
      }),
    ).rejects.toBeInstanceOf(SparkValidationError);
    expect(wallet.tryPayOverSpark).not.toHaveBeenCalled();
  });

  it("uses the supplied UUID for the Lightning transfer and SSP", async () => {
    const transferId = uuidv7obj();
    const transferIdString = transferId.toString();
    const prepareTransferForLightning = jest.fn<
      (
        leaves: unknown,
        paymentHash: Uint8Array,
        expiry: Date,
        id: string,
      ) => Promise<{ transferId: string }>
    >((_leaves, _paymentHash, _expiry, id) =>
      Promise.resolve({ transferId: id }),
    );
    const swapNodesForPreimage = jest.fn<
      (input: { startTransferRequest: { transferId: string } }) => Promise<{
        transfer: { id: string; leaves: Array<{ leaf: { id: string } }> };
      }>
    >(({ startTransferRequest }) =>
      Promise.resolve({
        transfer: {
          id: startTransferRequest.transferId,
          leaves: [{ leaf: { id: "leaf-id" } }],
        },
      }),
    );
    const requestLightningSend = jest.fn<
      (input: {
        encodedInvoice: string;
        amountSats?: number;
        userOutboundTransferExternalId: string;
      }) => Promise<{ id: string }>
    >(() => Promise.resolve({ id: "request" }));
    const wallet = Object.create(SparkWallet.prototype) as unknown as {
      config: {
        getNetwork: () => Network;
        getSspIdentityPublicKey: () => string;
      };
      getLightningSendFeeEstimate: () => Promise<number>;
      getSspClient: () => { requestLightningSend: typeof requestLightningSend };
      leafManager: {
        selectLeavesAndExecute: (
          amounts: number[],
          execute: (selected: Array<Array<{ id: string }>>) => Promise<unknown>,
        ) => Promise<unknown>;
        handleTransferEvent: (transfer: { id: string }) => Promise<void>;
      };
      lightningService: {
        swapNodesForPreimage: typeof swapNodesForPreimage;
      };
      payLightningInvoice: SparkWallet["payLightningInvoice"];
      transferService: {
        prepareTransferForLightning: typeof prepareTransferForLightning;
      };
    };
    wallet.config = {
      getNetwork: () => Network.MAINNET,
      getSspIdentityPublicKey: () => `02${"11".repeat(32)}`,
    };
    wallet.getLightningSendFeeEstimate = () => Promise.resolve(1);
    wallet.getSspClient = () => ({ requestLightningSend });
    wallet.transferService = { prepareTransferForLightning };
    wallet.lightningService = { swapNodesForPreimage };
    wallet.leafManager = {
      selectLeavesAndExecute: (_amounts, execute) =>
        execute([[{ id: "leaf-id" }]]),
      handleTransferEvent: () => Promise.resolve(),
    };

    await wallet.payLightningInvoice({
      invoice: LIGHTNING_INVOICE,
      maxFeeSats: 1,
      transferId,
    });

    expect(prepareTransferForLightning).toHaveBeenCalledWith(
      expect.anything(),
      expect.any(Uint8Array),
      expect.any(Date),
      transferIdString,
    );
    expect(swapNodesForPreimage).toHaveBeenCalledWith(
      expect.objectContaining({ idempotencyKey: transferIdString }),
    );
    expect(requestLightningSend).toHaveBeenCalledWith(
      expect.objectContaining({
        userOutboundTransferExternalId: transferIdString,
      }),
    );
  });

  it("releases leaves the SO did not take when a retry returns the original transfer", async () => {
    const transferId = uuidv7obj();
    const transferIdString = transferId.toString();
    const restoreLocalLockedToAvailable = jest.fn<(ids: string[]) => void>();
    const prepareTransferForLightning = jest.fn<
      (
        leaves: unknown,
        paymentHash: Uint8Array,
        expiry: Date,
        id: string,
      ) => Promise<{ transferId: string }>
    >((_leaves, _paymentHash, _expiry, id) =>
      Promise.resolve({ transferId: id }),
    );
    // The SO already holds this transfer ID from the first attempt, so it
    // returns that transfer — covering the leaves selected then, not the ones
    // selected now.
    const swapNodesForPreimage = jest.fn<
      () => Promise<{
        transfer: { id: string; leaves: Array<{ leaf: { id: string } }> };
      }>
    >(() =>
      Promise.resolve({
        transfer: {
          id: transferIdString,
          leaves: [{ leaf: { id: "first-attempt-leaf" } }],
        },
      }),
    );
    const requestLightningSend = jest.fn<() => Promise<{ id: string }>>(() =>
      Promise.resolve({ id: "request" }),
    );
    const wallet = Object.create(SparkWallet.prototype) as unknown as {
      config: {
        getNetwork: () => Network;
        getSspIdentityPublicKey: () => string;
      };
      getLightningSendFeeEstimate: () => Promise<number>;
      getSspClient: () => { requestLightningSend: typeof requestLightningSend };
      leafManager: {
        selectLeavesAndExecute: (
          amounts: number[],
          execute: (selected: Array<Array<{ id: string }>>) => Promise<unknown>,
        ) => Promise<unknown>;
        handleTransferEvent: (transfer: { id: string }) => Promise<void>;
        restoreLocalLockedToAvailable: typeof restoreLocalLockedToAvailable;
      };
      lightningService: { swapNodesForPreimage: typeof swapNodesForPreimage };
      logger: { warn: () => void };
      payLightningInvoice: SparkWallet["payLightningInvoice"];
      transferService: {
        prepareTransferForLightning: typeof prepareTransferForLightning;
      };
    };
    wallet.config = {
      getNetwork: () => Network.MAINNET,
      getSspIdentityPublicKey: () => `02${"11".repeat(32)}`,
    };
    wallet.logger = { warn: jest.fn() };
    wallet.getLightningSendFeeEstimate = () => Promise.resolve(1);
    wallet.getSspClient = () => ({ requestLightningSend });
    wallet.transferService = { prepareTransferForLightning };
    wallet.lightningService = { swapNodesForPreimage };
    wallet.leafManager = {
      selectLeavesAndExecute: (_amounts, execute) =>
        execute([[{ id: "retry-leaf" }]]),
      handleTransferEvent: () => Promise.resolve(),
      restoreLocalLockedToAvailable,
    };

    await wallet.payLightningInvoice({
      invoice: LIGHTNING_INVOICE,
      maxFeeSats: 1,
      transferId,
    });

    expect(restoreLocalLockedToAvailable).toHaveBeenCalledWith(["retry-leaf"]);
  });

  it("threads a supplied transfer ID down to the transfer service", async () => {
    const transferId = uuidv7obj().toString();
    const senderPublicKey = `02${"33".repeat(32)}`;
    const receiverPublicKey = `02${"22".repeat(32)}`;
    const fallbackAddress = encodeSparkAddress({
      identityPublicKey: receiverPublicKey,
      network: "MAINNET",
    });
    const sendTransferWithKeyTweaks = jest.fn<
      (
        leaves: LeafKeyTweak[],
        sparkInvoice?: string,
        transferId?: string,
      ) => Promise<Transfer>
    >(() =>
      Promise.resolve({
        id: transferId,
        senderIdentityPublicKey: hexToBytes(senderPublicKey),
        receiverIdentityPublicKey: hexToBytes(receiverPublicKey),
        status: 0,
        totalValue: 1300,
        leaves: [],
        type: 0,
        receivers: [],
        senders: [],
      } as unknown as Transfer),
    );
    const wallet = Object.create(SparkWallet.prototype) as unknown as {
      config: {
        getNetwork: () => Network;
        getNetworkType: () => string;
        signer: { getIdentityPublicKey: () => Promise<Uint8Array> };
      };
      leafManager: {
        selectLeavesAndExecute: (
          amounts: number[],
          execute: (selected: Array<Array<{ id: string }>>) => Promise<unknown>,
        ) => Promise<unknown>;
        handleTransferEvent: (transfer: { id: string }) => Promise<void>;
        restoreLocalLockedToAvailable: (ids: string[]) => void;
      };
      logger: { warn: () => void };
      transferService: {
        sendTransferWithKeyTweaks: typeof sendTransferWithKeyTweaks;
      };
      tryPayOverSpark: (
        invoice: DecodedInvoice,
        amountSats: number,
        network: Network,
        id: string,
      ) => Promise<unknown>;
    };
    wallet.logger = { warn: jest.fn() };
    wallet.config = {
      getNetwork: () => Network.MAINNET,
      getNetworkType: () => "MAINNET",
      signer: {
        getIdentityPublicKey: () =>
          Promise.resolve(hexToBytes(senderPublicKey)),
      },
    };
    wallet.leafManager = {
      selectLeavesAndExecute: (_amounts, execute) =>
        execute([[{ id: "leaf-id" }]]),
      handleTransferEvent: () => Promise.resolve(),
      restoreLocalLockedToAvailable: jest.fn(),
    };
    wallet.transferService = { sendTransferWithKeyTweaks };

    await wallet.tryPayOverSpark(
      {
        fallbackAddress,
        amountMSats: 1_300_000n,
        paymentHash: "00".repeat(32),
        signedPayeePubkey: "02".padEnd(66, "1"),
      },
      1300,
      Network.MAINNET,
      transferId,
    );

    expect(sendTransferWithKeyTweaks).toHaveBeenCalledWith(
      [expect.objectContaining({ leaf: { id: "leaf-id" } })],
      undefined,
      transferId,
    );
  });

  it("passes the UUID into an embedded Spark invoice transfer", async () => {
    const transferId = uuidv7obj().toString();
    const fulfillSparkInvoiceInternal = jest.fn<
      (
        invoice: string,
        amountSats: number,
        id: string,
      ) => Promise<{ id: string }>
    >(() => Promise.resolve({ id: transferId }));
    const wallet = Object.create(SparkWallet.prototype) as unknown as {
      fulfillSparkInvoiceInternal: typeof fulfillSparkInvoiceInternal;
      isCompatibleNetwork: () => boolean;
      logger: { warn: () => void };
      tryDecodeSparkAddress: () => {
        sparkInvoiceFields: {
          paymentType: { type: "sats"; amount: number };
        };
      };
      tryGetNetworkFromSparkAddress: () => "MAINNET";
      tryPayOverSpark: (
        invoice: DecodedInvoice,
        amountSats: number,
        network: Network,
        id: string,
      ) => Promise<unknown>;
      validateSparkInvoiceAmount: () => void;
    };
    wallet.logger = { warn: jest.fn() };
    wallet.tryGetNetworkFromSparkAddress = () => "MAINNET";
    wallet.isCompatibleNetwork = () => true;
    wallet.tryDecodeSparkAddress = () => ({
      sparkInvoiceFields: {
        paymentType: { type: "sats", amount: 1300 },
      },
    });
    wallet.validateSparkInvoiceAmount = jest.fn();
    wallet.fulfillSparkInvoiceInternal = fulfillSparkInvoiceInternal;

    await wallet.tryPayOverSpark(
      {
        fallbackAddress: "spark1invoice",
        amountMSats: 1_300_000n,
        paymentHash: "00".repeat(32),
        signedPayeePubkey: "02".padEnd(66, "1"),
      },
      1300,
      Network.MAINNET,
      transferId,
    );

    expect(fulfillSparkInvoiceInternal).toHaveBeenCalledWith(
      "spark1invoice",
      1300,
      transferId,
    );
  });

  it("passes the UUID into a regular Spark fallback transfer", async () => {
    const transferId = uuidv7obj().toString();
    const transferInternal = jest.fn<
      (
        params: { amountSats: number; receiverSparkAddress: string },
        id: string,
      ) => Promise<{ id: string }>
    >(() => Promise.resolve({ id: transferId }));
    const wallet = Object.create(SparkWallet.prototype) as unknown as {
      isCompatibleNetwork: () => boolean;
      logger: { warn: () => void };
      transfer: () => Promise<{ id: string }>;
      transferInternal: typeof transferInternal;
      tryDecodeSparkAddress: () => { identityPublicKey: string };
      tryGetNetworkFromSparkAddress: () => "MAINNET";
      tryPayOverSpark: (
        invoice: DecodedInvoice,
        amountSats: number,
        network: Network,
        id: string,
      ) => Promise<unknown>;
    };
    wallet.logger = { warn: jest.fn() };
    wallet.tryGetNetworkFromSparkAddress = () => "MAINNET";
    wallet.isCompatibleNetwork = () => true;
    wallet.tryDecodeSparkAddress = () => ({
      identityPublicKey: `02${"22".repeat(32)}`,
    });
    wallet.transfer = () => Promise.resolve({ id: "unexpected" });
    wallet.transferInternal = transferInternal;

    await wallet.tryPayOverSpark(
      {
        fallbackAddress: "spark1address",
        amountMSats: 1_300_000n,
        paymentHash: "00".repeat(32),
        signedPayeePubkey: "02".padEnd(66, "1"),
      },
      1300,
      Network.MAINNET,
      transferId,
    );

    expect(transferInternal).toHaveBeenCalledWith(
      {
        amountSats: 1300,
        receiverSparkAddress: "spark1address",
      },
      transferId,
    );
  });
});
