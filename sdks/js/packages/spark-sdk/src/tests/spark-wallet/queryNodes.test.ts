import { describe, it, expect, beforeEach, jest } from "@jest/globals";
import { SparkWallet } from "../../spark-wallet/spark-wallet.node.js";

/** Helper subclass to expose the private `queryNodes` method for testing. */
class TestSparkWallet extends SparkWallet {
  public constructor() {
    super();
  }

  /** Expose the private `queryNodes` method as public for tests. */
  public async queryNodesPublic(
    ...args: Parameters<SparkWallet["queryNodes"]>
  ) {
    return (this as any).queryNodes(...args);
  }

  /** Replace the internally-created ConnectionManager with a mocked version. */
  public setConnectionManager(manager: any) {
    this.connectionManager = manager;
  }
}

describe("queryNodes pagination", () => {
  let wallet: TestSparkWallet;
  let createSparkClientMock: jest.Mock;

  beforeEach(() => {
    const pageSize = 2;
    const paginatedResponses: Record<number, unknown> = {
      0: {
        nodes: {
          n1: { id: "n1" },
          n2: { id: "n2" },
        },
        offset: 0,
      },
      2: {
        nodes: {
          n2: { id: "n2" },
          n3: { id: "n3" },
        },
        offset: 2,
      },
      4: {
        nodes: {},
        offset: 4,
      },
    };

    // `query_nodes` implementation returns the matching response for the current offset.
    const queryNodesStub = jest.fn(async ({ offset }: { offset: number }) => {
      return paginatedResponses[offset] ?? { nodes: {}, offset };
    });

    // Mock `createSparkClient` so that each call returns an object containing the stub.
    createSparkClientMock = jest.fn(async () => ({
      query_nodes: queryNodesStub,
    }));

    // Mock ConnectionManager housing the mocked factory.
    const connectionManagerMock = {
      createSparkClient: createSparkClientMock,
    };

    wallet = new TestSparkWallet();
    wallet.setConnectionManager(connectionManagerMock);
  });

  it("aggregates all pages and removes duplicates", async () => {
    const result = await wallet.queryNodesPublic(
      { includeParents: false } as any,
      undefined,
      2,
    );

    // Expect three unique nodes in the final aggregation.
    expect(Object.keys(result.nodes)).toHaveLength(3);
    expect(Object.keys(result.nodes)).toEqual(
      expect.arrayContaining(["n1", "n2", "n3"]),
    );

    // Ensure we kept the last offset from the mocked response.
    expect(result.offset).toBe(4);

    // `createSparkClient` must have been invoked once per page (3 times).
    expect(createSparkClientMock).toHaveBeenCalledTimes(3);
  });
});
