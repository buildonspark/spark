/**
 * Unit tests for SparkReadonlyClient balance aggregation logic.
 */
import { hexToBytes } from "@noble/curves/utils";
import { describe, expect, it, jest } from "@jest/globals";
import { SparkRequestError, SparkValidationError } from "../../errors/types.js";
import {
  Network as SparkNetwork,
  type SparkServiceClient,
  TreeNodeStatus,
} from "../../proto/spark.js";
import { DefaultSparkSigner } from "../../signer/signer.js";
import { SparkReadonlyClient } from "../../spark-readonly-client/spark-readonly-client.js";
import { type WalletConfigService } from "../../services/config.js";
import { type AuthMode } from "../../services/connection/connection.js";
import { type ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";
import {
  ACTIVE_COUNTER_SWAP_STATUSES,
  COUNTER_SWAP_TYPES,
  OUTGOING_TRANSFER_TYPES,
  PRIMARY_SWAP_TYPES,
  SENDER_PENDING_STATUSES,
} from "../../services/transfer.js";
import { encodeSparkAddress } from "../../utils/address.js";

const TEST_COORDINATOR_IDENTIFIER = "test-coordinator";
const TEST_COORDINATOR_ADDRESS = "https://coordinator.test";
const TEST_IDENTITY_PUBLIC_KEY =
  "02ccb26ba79c63aaf60c9192fd874be3087ae8d8703275df0e558704a6d3a4f132";
const TEST_IDENTITY_PUBLIC_KEY_BYTES = hexToBytes(TEST_IDENTITY_PUBLIC_KEY);
const TEST_SPARK_ADDRESS = encodeSparkAddress({
  identityPublicKey: TEST_IDENTITY_PUBLIC_KEY,
  network: "LOCAL",
});

type QueryNodes = SparkServiceClient["query_nodes"];
type QueryAllTransfers = SparkServiceClient["query_all_transfers"];
type QueryNodesRequest = Parameters<QueryNodes>[0];
type QueryTransfersRequest = Parameters<QueryAllTransfers>[0];
type QueryNodesResponse = Awaited<ReturnType<QueryNodes>>;
type QueryTransfersResponse = Awaited<ReturnType<QueryAllTransfers>>;
type SenderTransferRequest = QueryTransfersRequest & {
  participant: {
    $case: "senderIdentityPublicKey";
    senderIdentityPublicKey: Uint8Array;
  };
};
type ReceiverTransferRequest = QueryTransfersRequest & {
  participant: {
    $case: "receiverIdentityPublicKey";
    receiverIdentityPublicKey: Uint8Array;
  };
};

let nextConnectionManager!: ConnectionManagerNodeJS;

class TestSparkReadonlyClient extends SparkReadonlyClient {
  protected override buildConnectionManager(
    _config: WalletConfigService,
    _authMode: AuthMode,
  ): ConnectionManagerNodeJS {
    return nextConnectionManager;
  }
}

function createTestClient(
  sparkClient: Pick<SparkServiceClient, "query_all_transfers" | "query_nodes">,
): {
  client: TestSparkReadonlyClient;
  createSparkClient: jest.Mock;
} {
  const createSparkClient = jest.fn(async () => sparkClient);
  nextConnectionManager = {
    createSparkClient,
  } as unknown as ConnectionManagerNodeJS;

  return {
    client: new TestSparkReadonlyClient(
      {
        network: "LOCAL",
        coordinatorIdentifier: TEST_COORDINATOR_IDENTIFIER,
        signingOperators: {
          [TEST_COORDINATOR_IDENTIFIER]: {
            id: 0,
            identifier: TEST_COORDINATOR_IDENTIFIER,
            address: TEST_COORDINATOR_ADDRESS,
            identityPublicKey: TEST_IDENTITY_PUBLIC_KEY,
          },
        },
      },
      new DefaultSparkSigner(),
      "identity",
    ),
    createSparkClient,
  };
}

function createNodePage(values: number[], offset: number) {
  return {
    nodes: Object.fromEntries(
      values.map((value, index) => [`node-${offset}-${index}`, { value }]),
    ),
    offset,
  };
}

function createTransfer(values: Array<number | undefined>) {
  return {
    leaves: values.map((value) =>
      value === undefined ? {} : { leaf: { value } },
    ),
  };
}

describe("SparkReadonlyClient.getOwnedBalance", () => {
  it("sums available leaves, pending outgoing leaves, and counter-swap leaves across paginated RPCs", async () => {
    const nodePages = [createNodePage([80, 20], 1), createNodePage([15], -1)];
    const senderPages = [
      {
        transfers: Array.from({ length: 100 }, (_, index) =>
          createTransfer(index === 0 ? [10, undefined] : [0]),
        ),
        offset: 1,
      },
      {
        transfers: [createTransfer([25]), createTransfer([undefined])],
        offset: -1,
      },
    ];
    const receiverPages = [
      {
        transfers: [createTransfer([7, 8]), createTransfer([undefined])],
        offset: -1,
      },
    ];

    const query_nodes = jest.fn<QueryNodes>().mockImplementation(async () => {
      await Promise.resolve();
      const nextPage = nodePages.shift();
      if (!nextPage) {
        throw new Error("No scripted node page remaining");
      }
      return nextPage as QueryNodesResponse;
    });

    const query_all_transfers = jest
      .fn<QueryAllTransfers>()
      .mockImplementation(async (filter) => {
        await Promise.resolve();
        const pages =
          filter.participant?.$case === "senderIdentityPublicKey"
            ? senderPages
            : filter.participant?.$case === "receiverIdentityPublicKey"
              ? receiverPages
              : undefined;
        if (!pages) {
          throw new Error("Unexpected transfer participant");
        }

        const nextPage = pages.shift();
        if (!nextPage) {
          throw new Error("No scripted transfer page remaining");
        }

        return nextPage as QueryTransfersResponse;
      });

    const { client, createSparkClient } = createTestClient({
      query_nodes,
      query_all_transfers,
    });

    await expect(client.getOwnedBalance(TEST_SPARK_ADDRESS)).resolves.toBe(
      165n,
    );

    expect(createSparkClient).toHaveBeenCalledWith(TEST_COORDINATOR_ADDRESS);

    expect(query_nodes).toHaveBeenCalledTimes(2);
    const firstNodeRequest = query_nodes.mock.calls[0]![0];
    const secondNodeRequest = query_nodes.mock.calls[1]![0];
    expect(firstNodeRequest.source?.$case).toBe("ownerIdentityPubkey");
    if (firstNodeRequest.source?.$case !== "ownerIdentityPubkey") {
      throw new Error("Expected node query to target ownerIdentityPubkey");
    }
    expect(firstNodeRequest.source.ownerIdentityPubkey).toEqual(
      TEST_IDENTITY_PUBLIC_KEY_BYTES,
    );
    expect(firstNodeRequest.statuses).toEqual([
      TreeNodeStatus.TREE_NODE_STATUS_AVAILABLE,
    ]);
    expect(firstNodeRequest.network).toBe(SparkNetwork.REGTEST);
    expect(firstNodeRequest.offset).toBe(0);
    expect(firstNodeRequest.limit).toBe(100);
    expect(firstNodeRequest.includeParents).toBe(false);
    expect(secondNodeRequest.offset).toBe(1);

    const senderRequests = query_all_transfers.mock.calls
      .map(([filter]) => filter)
      .filter(
        (filter): filter is SenderTransferRequest =>
          filter.participant?.$case === "senderIdentityPublicKey",
      );
    expect(senderRequests).toHaveLength(2);
    expect(senderRequests[0]!.participant.senderIdentityPublicKey).toEqual(
      TEST_IDENTITY_PUBLIC_KEY_BYTES,
    );
    expect(senderRequests[0]!.types).toEqual([
      ...OUTGOING_TRANSFER_TYPES,
      ...PRIMARY_SWAP_TYPES,
    ]);
    expect(senderRequests[0]!.statuses).toEqual(SENDER_PENDING_STATUSES);
    expect(senderRequests[0]!.network).toBe(SparkNetwork.REGTEST);
    expect(senderRequests[0]!.limit).toBe(100);
    expect(senderRequests[0]!.offset).toBe(0);
    expect(senderRequests[1]!.offset).toBe(1);

    const receiverRequests = query_all_transfers.mock.calls
      .map(([filter]) => filter)
      .filter(
        (filter): filter is ReceiverTransferRequest =>
          filter.participant?.$case === "receiverIdentityPublicKey",
      );
    expect(receiverRequests).toHaveLength(1);
    expect(receiverRequests[0]!.participant.receiverIdentityPublicKey).toEqual(
      TEST_IDENTITY_PUBLIC_KEY_BYTES,
    );
    expect(receiverRequests[0]!.types).toEqual(COUNTER_SWAP_TYPES);
    expect(receiverRequests[0]!.statuses).toEqual(ACTIVE_COUNTER_SWAP_STATUSES);
    expect(receiverRequests[0]!.network).toBe(SparkNetwork.REGTEST);
    expect(receiverRequests[0]!.limit).toBe(100);
    expect(receiverRequests[0]!.offset).toBe(0);
  });

  it("treats an empty first transfer page as complete even when the server returns a non-negative offset", async () => {
    const query_nodes = jest
      .fn<QueryNodes>()
      .mockResolvedValue(createNodePage([], -1) as QueryNodesResponse);
    const query_all_transfers = jest.fn<QueryAllTransfers>().mockResolvedValue({
      transfers: [],
      offset: 0,
    });

    const { client } = createTestClient({
      query_nodes,
      query_all_transfers,
    });

    await expect(client.getOwnedBalance(TEST_SPARK_ADDRESS)).resolves.toBe(0n);

    expect(query_all_transfers).toHaveBeenCalledTimes(2);
    expect(
      query_all_transfers.mock.calls.map(
        ([filter]) => filter.participant?.$case,
      ),
    ).toEqual(["senderIdentityPublicKey", "receiverIdentityPublicKey"]);
  });

  it("rejects invalid spark addresses before any RPC is attempted", async () => {
    const { client, createSparkClient } = createTestClient({
      query_nodes: jest.fn<QueryNodes>(),
      query_all_transfers: jest.fn<QueryAllTransfers>(),
    });

    let error: unknown;
    try {
      await client.getOwnedBalance("not-a-valid-spark-address");
    } catch (caught) {
      error = caught;
    }

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(createSparkClient).not.toHaveBeenCalled();
  });

  it("wraps repeated node offsets in SparkRequestError for available balance queries", async () => {
    const query_nodes = jest
      .fn<QueryNodes>()
      .mockResolvedValue(createNodePage([5], 0) as QueryNodesResponse);

    const { client } = createTestClient({
      query_nodes,
      query_all_transfers: jest.fn<QueryAllTransfers>(),
    });

    let error: unknown;
    try {
      await client.getAvailableBalance(TEST_SPARK_ADDRESS);
    } catch (caught) {
      error = caught;
    }

    expect(error).toBeInstanceOf(SparkRequestError);
    expect(query_nodes).toHaveBeenCalledTimes(1);

    const requestError = error as SparkRequestError;
    expect(requestError.originalError?.message).toContain(
      "Detected repeated offset while paginating node query",
    );
    expect(requestError.getContext().operation).toBe("query_nodes");
  });

  it("wraps transfer query failures in SparkRequestError while preserving the original error", async () => {
    const expectedError = new Error("query_all_transfers unavailable");
    const query_nodes = jest
      .fn<QueryNodes>()
      .mockResolvedValue(createNodePage([12], -1) as QueryNodesResponse);
    const query_all_transfers = jest
      .fn<QueryAllTransfers>()
      .mockImplementation(async (filter) => {
        await Promise.resolve();
        if (filter.participant?.$case === "senderIdentityPublicKey") {
          throw expectedError;
        }

        return {
          transfers: [],
          offset: -1,
        };
      });

    const { client } = createTestClient({
      query_nodes,
      query_all_transfers,
    });

    let error: unknown;
    try {
      await client.getOwnedBalance(TEST_SPARK_ADDRESS);
    } catch (caught) {
      error = caught;
    }

    expect(error).toBeInstanceOf(SparkRequestError);

    const requestError = error as SparkRequestError;
    expect(requestError.originalError).toBe(expectedError);
    expect(requestError.getContext().operation).toBe("query_all_transfers");
    expect(requestError.message).toContain("Failed to get owned balance");
  });

  it("wraps repeated transfer offsets in SparkRequestError for owned balance queries", async () => {
    const query_nodes = jest
      .fn<QueryNodes>()
      .mockResolvedValue(createNodePage([12], -1) as QueryNodesResponse);
    const query_all_transfers = jest
      .fn<QueryAllTransfers>()
      .mockImplementation(async (filter) => {
        await Promise.resolve();
        if (filter.participant?.$case === "senderIdentityPublicKey") {
          return {
            transfers: Array.from({ length: 100 }, () => createTransfer([1])),
            offset: 0,
          } as QueryTransfersResponse;
        }

        return {
          transfers: [],
          offset: -1,
        };
      });

    const { client } = createTestClient({
      query_nodes,
      query_all_transfers,
    });

    let error: unknown;
    try {
      await client.getOwnedBalance(TEST_SPARK_ADDRESS);
    } catch (caught) {
      error = caught;
    }

    expect(error).toBeInstanceOf(SparkRequestError);

    const requestError = error as SparkRequestError;
    expect(requestError.originalError?.message).toContain(
      "Detected repeated offset while paginating transfer query",
    );
    expect(requestError.getContext().operation).toBe("query_all_transfers");
    expect(requestError.message).toContain("Failed to get owned balance");
  });
});
