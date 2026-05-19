import { describe, expect, it, jest } from "@jest/globals";
import type { SparkSigner } from "../signer/signer.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";
import {
  Network,
  type QueryTransfersResponse,
  type Transfer,
  TransferStatus,
  TransferType,
} from "../proto/spark.js";
import type { ConnectionManager } from "../services/connection/connection.js";
import type { PendingTransferQueryOptions } from "../services/transfer.js";

const FIXED_SERVER_TIME = new Date("2026-04-23T22:30:15.125Z");

type QueryPendingTransfers = (
  options?: PendingTransferQueryOptions,
) => Promise<QueryTransfersResponse>;

type ClaimTransferForTesting = (args: {
  transfer: Transfer;
  emit?: boolean;
}) => Promise<string[]>;

type ClaimTransfersWalletInternals = {
  claimTransfer: jest.Mock<ClaimTransferForTesting>;
  claimTransfers: (types?: TransferType[], emit?: boolean) => Promise<string[]>;
  runPeriodicClaimTransfers: (types?: TransferType[], emit?: boolean) => void;
};

function claimTransfersWalletInternals(
  wallet: SparkWallet,
): ClaimTransfersWalletInternals {
  return wallet as unknown as ClaimTransfersWalletInternals;
}

class ClaimTransfersTestWallet extends SparkWallet {
  constructor(
    private readonly queryPendingTransfersStub: jest.Mock<QueryPendingTransfers>,
    private readonly claimTransferStub: jest.Mock<ClaimTransferForTesting> = jest.fn<ClaimTransferForTesting>(
      ({ transfer }) => Promise.resolve([transfer.id]),
    ),
    isTimeSyncedStub: jest.Mock<() => boolean> = jest.fn(() => true),
  ) {
    super({ network: "LOCAL" }, {} as SparkSigner);
    this.connectionManager.isTimeSynced = isTimeSyncedStub;
    this.transferService.queryPendingTransfers = queryPendingTransfersStub;
    claimTransfersWalletInternals(this).claimTransfer = claimTransferStub;
  }

  protected override buildConnectionManager() {
    return {
      closeConnections: () => Promise.resolve(),
      createSparkClient: () => Promise.resolve({}),
      getCurrentServerTime: () => FIXED_SERVER_TIME,
      isTimeSynced: () => true,
      subscribeToEvents: () =>
        Promise.reject(new Error("not used in claim transfer tests")),
    } as unknown as ConnectionManager;
  }

  public async runClaimTransfersForTesting(
    types?: TransferType[],
    emit?: boolean,
  ): Promise<string[]> {
    return await claimTransfersWalletInternals(this).claimTransfers(
      types,
      emit,
    );
  }
}

function createTransfer(
  id: string,
  type: TransferType = TransferType.TRANSFER,
  status: TransferStatus = TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED,
): Transfer {
  return {
    id,
    senderIdentityPublicKey: new Uint8Array([1]),
    receiverIdentityPublicKey: new Uint8Array([2]),
    status,
    totalValue: 1000,
    expiryTime: undefined,
    leaves: [],
    createdTime: undefined,
    updatedTime: undefined,
    type,
    sparkInvoice: "",
    network: Network.REGTEST,
    receivers: [],
  };
}

function createStatefulPendingTransferServer(initialTransfers: Transfer[]) {
  let pendingTransfers = [...initialTransfers];
  const queryPendingTransfersStub = jest.fn<QueryPendingTransfers>(
    ({ limit = 20, offset = 0 } = {}) => {
      const transfers = pendingTransfers.slice(offset, offset + limit);
      const nextOffset =
        offset + transfers.length < pendingTransfers.length
          ? offset + transfers.length
          : -1;

      return Promise.resolve({ transfers, offset: nextOffset });
    },
  );
  const claimTransferStub = jest.fn<ClaimTransferForTesting>(({ transfer }) => {
    pendingTransfers = pendingTransfers.filter(({ id }) => id !== transfer.id);

    return Promise.resolve([transfer.id]);
  });

  return {
    claimTransferStub,
    getPendingTransfers: () => pendingTransfers,
    queryPendingTransfersStub,
  };
}

describe("SparkWallet.claimTransfers", () => {
  it("drains pending transfers from the head in 25-transfer batches", async () => {
    const firstBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(`transfer-${i + 1}`),
    );
    const secondBatch = [createTransfer("transfer-26")];
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValueOnce({
        transfers: firstBatch,
        offset: 25,
      })
      .mockResolvedValueOnce({
        transfers: secondBatch,
        offset: -1,
      });

    const wallet = new ClaimTransfersTestWallet(queryPendingTransfersStub);

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(2);
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
    const mockCalls = queryPendingTransfersStub.mock.calls as unknown as Array<
      [{ createdBefore?: Date }]
    >;
    const firstQuery = mockCalls[0]?.[0];
    const secondQuery = mockCalls[1]?.[0];
    expect(secondQuery?.createdBefore).toBe(firstQuery?.createdBefore);
    expect(claimed).toHaveLength(26);
    expect(claimed[0]).toBe("transfer-1");
    expect(claimed[25]).toBe("transfer-26");
  });

  it("drains a shrinking server-side pending set across several 25-transfer batches", async () => {
    const transferCount = 76;
    const {
      claimTransferStub,
      getPendingTransfers,
      queryPendingTransfersStub,
    } = createStatefulPendingTransferServer(
      Array.from({ length: transferCount }, (_, i) =>
        createTransfer(`server-transfer-${i + 1}`),
      ),
    );

    const wallet = new ClaimTransfersTestWallet(
      queryPendingTransfersStub,
      claimTransferStub,
    );

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(claimed).toEqual(
      Array.from(
        { length: transferCount },
        (_, i) => `server-transfer-${i + 1}`,
      ),
    );
    expect(getPendingTransfers()).toEqual([]);
    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(4);
    expect(
      queryPendingTransfersStub.mock.calls.map(([options]) => options?.offset),
    ).toEqual([0, 0, 0, 0]);
    expect(
      queryPendingTransfersStub.mock.calls.map(([options]) => options?.limit),
    ).toEqual([25, 25, 25, 25]);
    expect(claimTransferStub).toHaveBeenCalledTimes(transferCount);
  });

  it("restarts from the head after partially claiming a full server-side batch", async () => {
    const skippedTransfer = createTransfer(
      "server-transfer-skipped",
      TransferType.TRANSFER,
      TransferStatus.TRANSFER_STATUS_EXPIRED,
    );
    const claimableTransferCount = 50;
    const {
      claimTransferStub,
      getPendingTransfers,
      queryPendingTransfersStub,
    } = createStatefulPendingTransferServer([
      skippedTransfer,
      ...Array.from({ length: claimableTransferCount }, (_, i) =>
        createTransfer(`server-transfer-${i + 1}`),
      ),
    ]);

    const wallet = new ClaimTransfersTestWallet(
      queryPendingTransfersStub,
      claimTransferStub,
    );

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(claimed).toEqual(
      Array.from(
        { length: claimableTransferCount },
        (_, i) => `server-transfer-${i + 1}`,
      ),
    );
    expect(getPendingTransfers()).toEqual([skippedTransfer]);
    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(3);
    expect(
      queryPendingTransfersStub.mock.calls.map(([options]) => options?.offset),
    ).toEqual([0, 0, 0]);
    expect(claimTransferStub).toHaveBeenCalledTimes(claimableTransferCount);
  });

  it("keeps filtered claim passes on a single snapshot", async () => {
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValue({
        transfers: [
          createTransfer("counter-swap", TransferType.COUNTER_SWAP),
          createTransfer("claimable-transfer", TransferType.TRANSFER),
        ],
        offset: -1,
      });

    const wallet = new ClaimTransfersTestWallet(queryPendingTransfersStub);

    const claimed = await wallet.runClaimTransfersForTesting([
      TransferType.TRANSFER,
    ]);

    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(1);
    expect(queryPendingTransfersStub).toHaveBeenCalledWith(
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
    expect(claimed).toEqual(["claimable-transfer"]);
  });

  it("skips non-claimable head batches to reach later claimable transfers", async () => {
    const firstBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(
        `expired-${i + 1}`,
        TransferType.TRANSFER,
        TransferStatus.TRANSFER_STATUS_EXPIRED,
      ),
    );
    const secondBatch = [createTransfer("claimable-later")];
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValueOnce({
        transfers: firstBatch,
        offset: 25,
      })
      .mockResolvedValueOnce({
        transfers: secondBatch,
        offset: -1,
      });

    const wallet = new ClaimTransfersTestWallet(queryPendingTransfersStub);

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(claimed).toEqual(["claimable-later"]);
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 25,
      }),
    );
  });

  it("skips fully failing head batches to reach later claimable transfers", async () => {
    const firstBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(`transfer-${i + 1}`),
    );
    const secondBatch = [createTransfer("transfer-26")];
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValueOnce({
        transfers: firstBatch,
        offset: 25,
      })
      .mockResolvedValueOnce({
        transfers: secondBatch,
        offset: -1,
      });
    const claimTransferStub = jest.fn<ClaimTransferForTesting>(
      ({ transfer }) => {
        if (transfer.id === "transfer-26") {
          return Promise.resolve([transfer.id]);
        }
        throw new Error(`failed to claim ${transfer.id}`);
      },
    );

    const wallet = new ClaimTransfersTestWallet(
      queryPendingTransfersStub,
      claimTransferStub,
    );

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(claimed).toEqual(["transfer-26"]);
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 25,
      }),
    );
    expect(claimTransferStub).toHaveBeenCalledTimes(26);
  });

  it("bounds the drain to transfers that existed when the pass started", async () => {
    const initialBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(`initial-${i + 1}`),
    );
    const liveTrafficBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(`live-${i + 1}`),
    );
    let callCount = 0;
    const queryPendingTransfersStub = jest.fn<QueryPendingTransfers>(
      ({ createdBefore } = {}) => {
        callCount++;
        if (callCount === 1) {
          return Promise.resolve({ transfers: initialBatch, offset: 25 });
        }

        if (createdBefore instanceof Date) {
          return Promise.resolve({ transfers: [], offset: -1 });
        }

        return Promise.resolve({ transfers: liveTrafficBatch, offset: 25 });
      },
    );

    const wallet = new ClaimTransfersTestWallet(queryPendingTransfersStub);

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(claimed).toHaveLength(25);
    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(2);
    const mockCalls = queryPendingTransfersStub.mock.calls as unknown as Array<
      [{ createdBefore?: Date }]
    >;
    const firstQuery = mockCalls[0]?.[0];
    const secondQuery = mockCalls[1]?.[0];
    expect(secondQuery?.createdBefore).toBe(firstQuery?.createdBefore);
  });

  it("warms up server time before taking a claim-drain snapshot", async () => {
    let isTimeSynced = false;
    const isTimeSyncedStub = jest.fn(() => isTimeSynced);
    const claimableTransfer = createTransfer("claimable-after-time-sync");
    const queryPendingTransfersStub = jest.fn<QueryPendingTransfers>(
      ({ createdBefore } = {}) => {
        if (createdBefore == null) {
          isTimeSynced = true;
          return Promise.resolve({ transfers: [], offset: -1 });
        }

        return Promise.resolve({
          transfers: [claimableTransfer],
          offset: -1,
        });
      },
    );

    const wallet = new ClaimTransfersTestWallet(
      queryPendingTransfersStub,
      undefined,
      isTimeSyncedStub,
    );

    const claimed = await wallet.runClaimTransfersForTesting();

    expect(claimed).toEqual(["claimable-after-time-sync"]);
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(1, {
      limit: 1,
      offset: 0,
    });
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
  });

  it("falls back to first-page draining when server time warm-up fails", async () => {
    const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});
    const claimableTransfer = createTransfer("claimable-after-warmup-failure");
    const queryPendingTransfersStub = jest
      .fn<QueryPendingTransfers>()
      .mockRejectedValueOnce(new Error("warm-up failed"))
      .mockResolvedValueOnce({
        transfers: [claimableTransfer],
        offset: -1,
      });

    try {
      const wallet = new ClaimTransfersTestWallet(
        queryPendingTransfersStub,
        undefined,
        jest.fn(() => false),
      );

      const claimed = await wallet.runClaimTransfersForTesting();

      expect(claimed).toEqual(["claimable-after-warmup-failure"]);
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(1, {
        limit: 1,
        offset: 0,
      });
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
        2,
        expect.objectContaining({
          createdBefore: undefined,
          limit: 25,
          offset: 0,
        }),
      );
      expect(warnSpy).toHaveBeenCalledWith(
        expect.stringContaining(
          "Unable to warm up server time for pending-transfer claim snapshot; falling back to first-page drain",
        ),
        expect.any(Error),
      );
    } finally {
      warnSpy.mockRestore();
    }
  });

  it("keeps draining the first page while fallback batches make progress", async () => {
    const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});
    const isTimeSyncedStub = jest.fn(() => false);
    const firstBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(`fallback-${i + 1}`),
    );
    const secondBatch = [createTransfer("fallback-26")];
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValueOnce({
        transfers: [],
        offset: -1,
      })
      .mockResolvedValueOnce({
        transfers: firstBatch,
        offset: 25,
      })
      .mockResolvedValueOnce({
        transfers: secondBatch,
        offset: -1,
      });

    try {
      const wallet = new ClaimTransfersTestWallet(
        queryPendingTransfersStub,
        undefined,
        isTimeSyncedStub,
      );

      const claimed = await wallet.runClaimTransfersForTesting();

      expect(claimed).toHaveLength(26);
      expect(queryPendingTransfersStub).toHaveBeenCalledTimes(3);
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(1, {
        limit: 1,
        offset: 0,
      });
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
        2,
        expect.objectContaining({
          createdBefore: undefined,
          limit: 25,
          offset: 0,
        }),
      );
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
        3,
        expect.objectContaining({
          createdBefore: undefined,
          limit: 25,
          offset: 0,
        }),
      );
    } finally {
      warnSpy.mockRestore();
    }
  });

  it("scans later fallback pages when the head batch is unclaimable", async () => {
    const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});
    const isTimeSyncedStub = jest.fn(() => false);
    const firstBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(
        `fallback-expired-${i + 1}`,
        TransferType.TRANSFER,
        TransferStatus.TRANSFER_STATUS_EXPIRED,
      ),
    );
    const secondBatch = [createTransfer("fallback-claimable-later")];
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValueOnce({
        transfers: [],
        offset: -1,
      })
      .mockResolvedValueOnce({
        transfers: firstBatch,
        offset: 25,
      })
      .mockResolvedValueOnce({
        transfers: secondBatch,
        offset: -1,
      });

    try {
      const wallet = new ClaimTransfersTestWallet(
        queryPendingTransfersStub,
        undefined,
        isTimeSyncedStub,
      );

      const claimed = await wallet.runClaimTransfersForTesting();

      expect(claimed).toEqual(["fallback-claimable-later"]);
      expect(queryPendingTransfersStub).toHaveBeenCalledTimes(3);
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
        2,
        expect.objectContaining({
          createdBefore: undefined,
          limit: 25,
          offset: 0,
        }),
      );
      expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
        3,
        expect.objectContaining({
          createdBefore: undefined,
          limit: 25,
          offset: 25,
        }),
      );
    } finally {
      warnSpy.mockRestore();
    }
  });

  it("bounds fallback scans through unclaimable pages", async () => {
    const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});
    const isTimeSyncedStub = jest.fn(() => false);
    const fullBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(
        `fallback-expired-loop-${i + 1}`,
        TransferType.TRANSFER,
        TransferStatus.TRANSFER_STATUS_EXPIRED,
      ),
    );
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValue({
        transfers: fullBatch,
        offset: 25,
      });

    try {
      const wallet = new ClaimTransfersTestWallet(
        queryPendingTransfersStub,
        undefined,
        isTimeSyncedStub,
      );

      const claimed = await wallet.runClaimTransfersForTesting();

      expect(claimed).toEqual([]);
      expect(queryPendingTransfersStub).toHaveBeenCalledTimes(101);
    } finally {
      warnSpy.mockRestore();
    }
  });

  it("bounds the fallback drain when server time remains unavailable", async () => {
    const warnSpy = jest.spyOn(console, "warn").mockImplementation(() => {});
    const isTimeSyncedStub = jest.fn(() => false);
    const fullBatch = Array.from({ length: 25 }, (_, i) =>
      createTransfer(`fallback-loop-${i + 1}`),
    );
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockResolvedValue({
        transfers: fullBatch,
        offset: 25,
      });

    try {
      const wallet = new ClaimTransfersTestWallet(
        queryPendingTransfersStub,
        undefined,
        isTimeSyncedStub,
      );

      const claimed = await wallet.runClaimTransfersForTesting();

      expect(claimed).toHaveLength(25 * 100);
      expect(queryPendingTransfersStub).toHaveBeenCalledTimes(101);
    } finally {
      warnSpy.mockRestore();
    }
  });

  it("skips overlapping periodic claim passes", async () => {
    let resolveFirstQuery: (response: QueryTransfersResponse) => void;
    const firstQuery = new Promise<QueryTransfersResponse>((resolve) => {
      resolveFirstQuery = resolve;
    });
    const queryPendingTransfersStub = jest
      .fn<() => Promise<QueryTransfersResponse>>()
      .mockReturnValueOnce(firstQuery)
      .mockResolvedValue({
        transfers: [],
        offset: -1,
      });
    const wallet = new ClaimTransfersTestWallet(queryPendingTransfersStub);

    const internals = claimTransfersWalletInternals(wallet);

    internals.runPeriodicClaimTransfers();
    await Promise.resolve();
    internals.runPeriodicClaimTransfers([TransferType.TRANSFER], true);

    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(1);

    resolveFirstQuery!({
      transfers: [],
      offset: -1,
    });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));

    internals.runPeriodicClaimTransfers([TransferType.TRANSFER], true);
    await Promise.resolve();

    expect(queryPendingTransfersStub).toHaveBeenCalledTimes(2);
    expect(queryPendingTransfersStub).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        createdBefore: FIXED_SERVER_TIME,
        limit: 25,
        offset: 0,
      }),
    );
  });
});
