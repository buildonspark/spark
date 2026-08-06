import { describe, expect, it, jest } from "@jest/globals";
import { UUID } from "uuidv7";
import type {
  StartTransferRequest,
  StartTransferResponse,
  Transfer,
} from "../proto/spark.js";
import { KeyDerivationType } from "../signer/types.js";
import { type LeafKeyTweak, TransferService } from "../services/transfer.js";
import type { SparkCallOptions } from "../types/grpc.js";

const TRANSFER_ID = "018f47f2-1c2d-7abc-8def-0123456789ab";

function createTransferService() {
  const startTransfer = jest.fn<
    (
      request: StartTransferRequest,
      options?: SparkCallOptions,
    ) => Promise<StartTransferResponse>
  >(() =>
    Promise.resolve({
      transfer: { id: TRANSFER_ID } as unknown as Transfer,
      signingResults: [],
    }),
  );
  const service = Object.create(
    TransferService.prototype,
  ) as unknown as TransferService;
  Reflect.set(
    service,
    "prepareSendTransferKeyTweaks",
    jest.fn(() => Promise.resolve(new Map())),
  );
  Reflect.set(
    service,
    "prepareTransferPackage",
    jest.fn(() => Promise.resolve({})),
  );
  Reflect.set(service, "connectionManager", {
    createSparkClient: jest.fn(() =>
      Promise.resolve({ start_transfer_v2: startTransfer }),
    ),
  });
  Reflect.set(service, "config", {
    getCoordinatorAddress: jest.fn(() => "coordinator"),
    signer: {
      getIdentityPublicKey: jest.fn(() => Promise.resolve(new Uint8Array([2]))),
    },
  });

  return { service, startTransfer };
}

const leafKeyTweak = {
  leaf: { id: "leaf-id" },
  keyDerivation: {
    type: KeyDerivationType.LEAF,
    path: "leaf-id",
  },
  newKeyDerivation: {
    type: KeyDerivationType.RANDOM,
  },
  receiverIdentityPublicKey: new Uint8Array([3]),
} as LeafKeyTweak;

describe("Lightning fallback transfer de-duplication", () => {
  it("uses a supplied transfer ID without transport idempotency metadata", async () => {
    const { service, startTransfer } = createTransferService();

    await service.sendTransferWithKeyTweaks(
      [leafKeyTweak],
      undefined,
      TRANSFER_ID,
    );

    const [request, options] = startTransfer.mock.calls[0]!;
    expect(request.transferId).toBe(TRANSFER_ID);
    expect(options).toBeUndefined();
  });

  it("generates an ID without metadata when none is supplied", async () => {
    const { service, startTransfer } = createTransferService();

    await service.sendTransferWithKeyTweaks([leafKeyTweak]);

    const [request, options] = startTransfer.mock.calls[0]!;
    expect(() => UUID.parse(request.transferId)).not.toThrow();
    expect(options).toBeUndefined();
  });
});
