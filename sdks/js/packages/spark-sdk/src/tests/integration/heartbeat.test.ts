import { describe, expect, it } from "@jest/globals";
import type { SubscribeToEventsResponse } from "../../proto/spark.js";
import type { ConfigOptions } from "../../services/wallet-config.js";
import { SparkWalletEvent } from "../../spark-wallet/types.js";
import { walletTypes } from "../test-utils.js";
import {
  SparkWalletTestingIntegration,
  SparkWalletTestingIntegrationWithStream,
} from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

async function waitForTransferClaim(
  wallet: SparkWalletTestingIntegrationWithStream,
  transferId: string,
  timeoutMs: number,
): Promise<{ transferId: string; balance: bigint }> {
  await Promise.resolve();
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => {
      wallet.removeListener(SparkWalletEvent.TransferClaimed, handler);
      reject(
        new Error(`Timeout waiting for transfer ${transferId} to be claimed`),
      );
    }, timeoutMs);

    const handler = (claimedTransferId: string, balance: bigint) => {
      if (claimedTransferId === transferId) {
        clearTimeout(timeout);
        wallet.removeListener(SparkWalletEvent.TransferClaimed, handler);
        resolve({ transferId: claimedTransferId, balance });
      }
    };

    wallet.on(SparkWalletEvent.TransferClaimed, handler);
  });
}

async function waitForStreamEvent(
  iterator: AsyncIterator<SubscribeToEventsResponse>,
  timeoutMs: number,
) {
  let timeout: ReturnType<typeof setTimeout> | undefined;
  const event = await Promise.race([
    iterator.next(),
    new Promise<never>((_, reject) => {
      timeout = setTimeout(
        () =>
          reject(
            new Error(`Timeout waiting for stream event after ${timeoutMs}ms`),
          ),
        timeoutMs,
      );
    }),
  ]);
  if (timeout) {
    clearTimeout(timeout);
  }
  if (event.done || !event.value) {
    throw new Error("Stream ended before delivering the next event");
  }
  return event.value;
}

describe.each(walletTypes)(
  "subscribe_to_events heartbeat",
  ({ name, Signer }) => {
    it(`${name} - emits heartbeat events on an idle stream`, async () => {
      const { wallet } = await SparkWalletTestingIntegration.initialize({
        options: {
          network: "LOCAL",
        },
        signer: new Signer(),
      });

      const abortController = new AbortController();
      try {
        const configService = wallet.getConfigService();
        const connectionManager = wallet.getConnectionManager();
        const stream = await connectionManager.subscribeToEvents(
          configService.getCoordinatorAddress(),
          abortController.signal,
        );
        const iterator = stream[Symbol.asyncIterator]();

        const connectedEvent = await waitForStreamEvent(iterator, 5_000);
        expect(connectedEvent.event?.$case).toBe("connected");

        const heartbeatEvent = await waitForStreamEvent(iterator, 10_000);
        expect(heartbeatEvent.event?.$case).toBe("heartbeat");
      } finally {
        abortController.abort();
        await wallet.cleanupConnections();
      }
    }, 20_000);

    it(`${name} - keeps an idle background stream healthy with heartbeats`, async () => {
      const faucet = BitcoinFaucet.getInstance();
      const options: ConfigOptions = {
        network: "LOCAL",
      };

      let senderWallet: SparkWalletTestingIntegration | undefined;
      let receiverWallet: SparkWalletTestingIntegrationWithStream | undefined;

      try {
        ({ wallet: senderWallet } =
          await SparkWalletTestingIntegration.initialize({
            options,
            signer: new Signer(),
          }));

        const depositAddress = await senderWallet.getSingleUseDepositAddress();
        const signedTx = await faucet.sendToAddress(depositAddress, 1_000n);
        await faucet.mineBlocksAndWaitForMiningToComplete(3);
        await senderWallet.claimDeposit(signedTx.id);

        let streamConnectedCount = 0;
        const reconnectEvents: Array<{
          attempt: number;
          delayMs: number;
          error: string;
        }> = [];
        let disconnectedReason: string | undefined;
        let resolveStreamConnected!: () => void;
        const streamConnectedPromise = new Promise<void>((resolve) => {
          resolveStreamConnected = resolve;
        });

        ({ wallet: receiverWallet } =
          await SparkWalletTestingIntegrationWithStream.initialize({
            options: {
              ...options,
              events: {
                [SparkWalletEvent.StreamConnected]: () => {
                  streamConnectedCount += 1;
                  resolveStreamConnected();
                },
                [SparkWalletEvent.StreamReconnecting]: (
                  attempt,
                  _maxAttempts,
                  delayMs,
                  error,
                ) => {
                  reconnectEvents.push({ attempt, delayMs, error });
                },
                [SparkWalletEvent.StreamDisconnected]: (reason) => {
                  disconnectedReason = reason;
                },
              },
            },
            signer: new Signer(),
          }));

        await streamConnectedPromise;

        // Wait longer than the SDK heartbeat watchdog to prove heartbeats keep
        // the stream alive instead of forcing an idle reconnect.
        await new Promise((resolve) => setTimeout(resolve, 18_000));

        expect(streamConnectedCount).toBe(1);
        expect(reconnectEvents).toHaveLength(0);
        expect(disconnectedReason).toBeUndefined();

        const transfer = await senderWallet.transfer({
          amountSats: 1000,
          receiverSparkAddress: await receiverWallet.getSparkAddress(),
        });

        const result = await waitForTransferClaim(
          receiverWallet,
          transfer.id,
          10_000,
        );
        expect(result.transferId).toBe(transfer.id);
        expect(result.balance).toBe(1000n);

        const receiverBalance = await receiverWallet.getBalance();
        expect(receiverBalance.balance).toBe(1000n);
        expect(reconnectEvents).toHaveLength(0);
        expect(disconnectedReason).toBeUndefined();
      } finally {
        await Promise.all([
          senderWallet?.cleanupConnections(),
          receiverWallet?.cleanupConnections(),
        ]);
      }
    }, 90_000);
  },
);
