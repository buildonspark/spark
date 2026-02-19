import { numberToBytesBE } from "@noble/curves/utils";
import { describe, expect, it, jest } from "@jest/globals";
import {
  OutputWithPreviousTransactionData,
  TokenOutputStatus,
} from "../proto/spark_token.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { TokenOutputManager } from "../services/tokens/output-manager.js";
import { TokenTransactionService } from "../services/tokens/token-transactions.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.node.js";
import { encodeSparkAddress } from "../utils/address.js";
import { encodeBech32mTokenIdentifier } from "../utils/token-identifier.js";

let nextConnectionManager!: ConnectionManagerNodeJS;

class TestSparkWallet extends SparkWallet {
  protected override buildConnectionManager(
    _config: WalletConfigService,
  ): ConnectionManagerNodeJS {
    return nextConnectionManager;
  }

  public constructor(lockExpiryMs: number = 20000) {
    super({
      network: "LOCAL",
      tokenOutputLockExpiryMs: lockExpiryMs,
    });
  }

  public async initializeSignerForTest(): Promise<void> {
    await this.config.signer.createSparkWalletFromSeed(
      new Uint8Array(32).fill(1),
      0,
    );
  }

  public getTokenTransactionServiceForTest(): TokenTransactionService {
    return this.tokenTransactionService;
  }
}

function createMockTokenOutput({
  id,
  tokenIdentifier,
  tokenAmount,
  status = TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
}: {
  id: string;
  tokenIdentifier: Uint8Array;
  tokenAmount: bigint;
  status?: TokenOutputStatus;
}): OutputWithPreviousTransactionData {
  return {
    output: {
      id,
      ownerPublicKey: new Uint8Array(32).fill(1),
      tokenPublicKey: new Uint8Array(32).fill(2),
      tokenIdentifier,
      tokenAmount: numberToBytesBE(tokenAmount, 16),
      revocationCommitment: new Uint8Array(32).fill(3),
      status,
    },
    previousTransactionHash: new Uint8Array(32).fill(4),
    previousTransactionVout: 0,
  };
}

const TEST_IDENTITY_PUBKEY =
  "02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132";

async function createWalletWithScript({
  outputSnapshots,
  transferResults,
  lockExpiryMs,
}: {
  outputSnapshots: OutputWithPreviousTransactionData[][];
  transferResults: Array<string | Error>;
  lockExpiryMs?: number;
}): Promise<TestSparkWallet> {
  const scriptedSnapshots = [...outputSnapshots];
  const tokenClient = {
    query_token_outputs: jest.fn(async () => {
      const snapshot = scriptedSnapshots.shift();
      if (!snapshot) {
        throw new Error("No scripted output snapshot remaining");
      }
      return {
        outputsWithPreviousTransactionData: snapshot,
      };
    }),
  };

  nextConnectionManager = {
    createSparkTokenClient: jest.fn(async () => tokenClient),
    getCurrentServerTime: jest.fn(() => new Date("2026-01-01T00:00:00.000Z")),
  } as unknown as ConnectionManagerNodeJS;

  const wallet = new TestSparkWallet(lockExpiryMs);
  await wallet.initializeSignerForTest();

  const scriptedTransferResults = [...transferResults];
  jest
    .spyOn(
      wallet.getTokenTransactionServiceForTest(),
      "broadcastTokenTransactionV3",
    )
    .mockImplementation(async () => {
      const nextResult = scriptedTransferResults.shift();
      if (nextResult === undefined) {
        throw new Error("No scripted transfer result remaining");
      }
      if (nextResult instanceof Error) {
        throw nextResult;
      }
      return nextResult;
    });

  return wallet;
}

function createReceiverSparkAddress(): string {
  return encodeSparkAddress({
    identityPublicKey: TEST_IDENTITY_PUBKEY,
    network: "LOCAL",
  });
}

describe("token transfer local lock lifecycle", () => {
  it("should remove local lock when output becomes pending on server", async () => {
    const manager = new TokenOutputManager();
    const tokenIdentifierBytes = new Uint8Array(32).fill(5);
    const tokenIdentifier = encodeBech32mTokenIdentifier({
      tokenIdentifier: tokenIdentifierBytes,
      network: "LOCAL",
    });

    const initialOutputs = [
      createMockTokenOutput({
        id: "out1",
        tokenIdentifier: tokenIdentifierBytes,
        tokenAmount: 100n,
      }),
      createMockTokenOutput({
        id: "out2",
        tokenIdentifier: tokenIdentifierBytes,
        tokenAmount: 100n,
      }),
    ];
    await manager.setOutputs(new Map([[tokenIdentifier, initialOutputs]]));

    await manager.lockOutputsByIds(["out1"]);
    const initialPending =
      await manager.getPendingOutboundOutputs(tokenIdentifier);
    expect(initialPending).toHaveLength(1);
    expect(initialPending[0]!.output!.id).toBe("out1");

    const updatedOutputs = [
      createMockTokenOutput({
        id: "out1",
        tokenIdentifier: tokenIdentifierBytes,
        tokenAmount: 100n,
        status: TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
      }),
      createMockTokenOutput({
        id: "out2",
        tokenIdentifier: tokenIdentifierBytes,
        tokenAmount: 100n,
      }),
    ];
    await manager.setOutputs(new Map([[tokenIdentifier, updatedOutputs]]));

    const available = await manager.getAvailableOutputs(tokenIdentifier);
    expect(available).toHaveLength(1);
    expect(available[0]!.output!.id).toBe("out2");

    const pending = await manager.getPendingOutboundOutputs(tokenIdentifier);
    expect(pending).toHaveLength(1);
    expect(pending[0]!.output!.id).toBe("out1");

    await manager.setOutputs(new Map([[tokenIdentifier, initialOutputs]]));

    const finalPending =
      await manager.getPendingOutboundOutputs(tokenIdentifier);
    expect(finalPending).toHaveLength(0);
  });

  it("prevents immediate respend on stale server data after a successful transfer", async () => {
    const tokenIdentifierBytes = new Uint8Array(32).fill(7);
    const tokenIdentifier = encodeBech32mTokenIdentifier({
      tokenIdentifier: tokenIdentifierBytes,
      network: "LOCAL",
    });

    const wallet = await createWalletWithScript({
      outputSnapshots: [
        [
          createMockTokenOutput({
            id: "out-1",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 100n,
          }),
        ],
        [
          createMockTokenOutput({
            id: "out-1",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 100n,
          }),
        ],
        [
          createMockTokenOutput({
            id: "out-1",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 100n,
            status: TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND,
          }),
        ],
        [
          createMockTokenOutput({
            id: "out-1",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 100n,
          }),
        ],
      ],
      transferResults: ["tx-hash-1", "tx-hash-2"],
    });
    const receiverSparkAddress = createReceiverSparkAddress();

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 100n,
        receiverSparkAddress,
      }),
    ).resolves.toBe("tx-hash-1");

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 100n,
        receiverSparkAddress,
      }),
    ).rejects.toThrow("Insufficient token amount");

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 100n,
        receiverSparkAddress,
      }),
    ).rejects.toThrow("Insufficient token amount");

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 100n,
        receiverSparkAddress,
      }),
    ).resolves.toBe("tx-hash-2");
  });

  it("keeps outputs unavailable after transfer broadcast failure until lock expiry or server update", async () => {
    const tokenIdentifierBytes = new Uint8Array(32).fill(9);
    const tokenIdentifier = encodeBech32mTokenIdentifier({
      tokenIdentifier: tokenIdentifierBytes,
      network: "LOCAL",
    });

    const wallet = await createWalletWithScript({
      outputSnapshots: [
        [
          createMockTokenOutput({
            id: "out-2",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 200n,
          }),
        ],
        [
          createMockTokenOutput({
            id: "out-2",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 200n,
          }),
        ],
      ],
      transferResults: [new Error("broadcast failed")],
    });
    const receiverSparkAddress = createReceiverSparkAddress();

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 200n,
        receiverSparkAddress,
      }),
    ).rejects.toThrow("broadcast failed");

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 200n,
        receiverSparkAddress,
      }),
    ).rejects.toThrow("Insufficient token amount");
  });

  it("releases local lock after configured expiry and allows re-spend", async () => {
    const tokenIdentifierBytes = new Uint8Array(32).fill(11);
    const tokenIdentifier = encodeBech32mTokenIdentifier({
      tokenIdentifier: tokenIdentifierBytes,
      network: "LOCAL",
    });

    const wallet = await createWalletWithScript({
      outputSnapshots: [
        [
          createMockTokenOutput({
            id: "out-3",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 300n,
          }),
        ],
        [
          createMockTokenOutput({
            id: "out-3",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 300n,
          }),
        ],
        [
          createMockTokenOutput({
            id: "out-3",
            tokenIdentifier: tokenIdentifierBytes,
            tokenAmount: 300n,
          }),
        ],
      ],
      transferResults: ["tx-hash-ttl-1", "tx-hash-ttl-2"],
      lockExpiryMs: 50,
    });
    const receiverSparkAddress = createReceiverSparkAddress();

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 300n,
        receiverSparkAddress,
      }),
    ).resolves.toBe("tx-hash-ttl-1");

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 300n,
        receiverSparkAddress,
      }),
    ).rejects.toThrow("Insufficient token amount");

    await new Promise((resolve) => setTimeout(resolve, 70));

    await expect(
      wallet.transferTokens({
        tokenIdentifier,
        tokenAmount: 300n,
        receiverSparkAddress,
      }),
    ).resolves.toBe("tx-hash-ttl-2");
  });
});
