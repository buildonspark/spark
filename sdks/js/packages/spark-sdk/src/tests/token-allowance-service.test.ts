import { numberToBytesBE } from "@noble/curves/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import {
  hashCreateTokenAllowancePayload,
  hashRevokeTokenAllowancePayload,
} from "../utils/token-allowance-hashing.js";
import { jest } from "@jest/globals";
import { SparkValidationError } from "../errors/types.js";
import { SparkTokenPrimitives } from "../token-primitives-bindings/token-primitives-bindings.node.js";
import { setSparkTokenPrimitivesOnce } from "../token-primitives-bindings/token-primitives-bindings.js";
import {
  type FinalTokenTransaction,
  type OutputWithPreviousTransactionData,
  TokenAllowanceStatus,
  type TokenAllowanceInfo,
  type TokenAllowancePayload,
  TokenOutputStatus,
} from "../proto/spark_token.js";
import { WalletConfigService } from "../services/config.js";
import { type ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import {
  ALLOWANCE_QUERY_MAX_PAGES,
  TokenAllowanceService,
  type AllowanceCeiling,
} from "../services/tokens/allowances.js";
import { tokenTransactionCallOptions } from "../services/tokens/token-transactions.js";
import { WalletConfig } from "../services/wallet-config.js";
import { UnsafeStatelessSparkSigner } from "../signer/signer.js";

const SPENDER_SEED = new Uint8Array(32).fill(7);
const ownerPrivateKey = new Uint8Array(32).fill(3);
const ownerPublicKey = secp256k1.getPublicKey(ownerPrivateKey, true);
const OWNER_KEY_HEX = bytesToHex(ownerPublicKey);
const RECIPIENT_HEX =
  "0375a9121cd7c3684ca1941978cc0dc42ce316fddf70261643f17ba3eeca6d10f2";
const ALLOWANCE_ID_HEX = "0123456789abcdef0123456789abcdef";
const TOKEN_ID_HEX =
  "3e534a8d9798fe5e20516f9b1aa05f5d78d718ece893e8af89d678c3d88f2451";
const PER_TX_CAP_HEX = "00000000000000000000000000002710"; // 10000
const TOTAL_LIMIT_HEX = "000000000000000000000000000186a0"; // 100000
const ZERO_U128_HEX = "00000000000000000000000000000000";
const FUTURE_EXPIRY = new Date("2030-01-01T00:00:00.000Z");

type QueryAllowancesRequest = {
  ownerPublicKey?: Uint8Array;
  spenderPublicKey?: Uint8Array;
  tokenIdentifier?: Uint8Array;
  includeInactive?: boolean;
  limit?: number;
  offset?: number;
};

type AllowancePage = { allowances: TokenAllowanceInfo[]; offset: number };

/**
 * A page the SO reports as non-final: it returns the next offset only after
 * finding a row beyond a full page, so a continuing page always holds
 * ALLOWANCE_QUERY_PAGE_SIZE rows.
 */
function pageWithMore(
  nextOffset: number,
  ...tail: TokenAllowanceInfo[]
): AllowancePage {
  const filler = Array.from({ length: PAGE_SIZE - tail.length }, (_unused, i) =>
    allowanceInfo({
      allowancePayload: allowancePayload({
        allowanceId: numberToBytesBE(BigInt(i + 1), 16),
      }),
    }),
  );
  return { allowances: [...tail, ...filler], offset: nextOffset };
}

function finalPage(...allowances: TokenAllowanceInfo[]): AllowancePage {
  return { allowances, offset: -1 };
}

const PAGE_SIZE = 100;

type Fixture = {
  service: TokenAllowanceService;
  queryRequests: QueryAllowancesRequest[];
  createCallOptions: unknown[];
  broadcastCallOptions: unknown[];
};

setSparkTokenPrimitivesOnce(new SparkTokenPrimitives());

let spenderPublicKey: Uint8Array;
let spenderPublicKeyHex: string;
let operatorPublicKeys: Uint8Array[];

beforeAll(async () => {
  const signer = new UnsafeStatelessSparkSigner();
  await signer.createSparkWalletFromSeed(SPENDER_SEED, 0);
  spenderPublicKey = await signer.getIdentityPublicKey();
  spenderPublicKeyHex = bytesToHex(spenderPublicKey);
  operatorPublicKeys = Object.values(
    new WalletConfigService(
      { ...WalletConfig.LOCAL, network: "LOCAL" },
      signer,
    ).getSigningOperators(),
  ).map((operator) => hexToBytes(operator.identityPublicKey));
});

async function createService({
  allowancePages = [],
  ownedOutputs = [],
  broadcastResults = [],
  createAllowanceResults = [],
  serverTime = () => new Date(),
}: {
  serverTime?: () => Date;
  allowancePages?: AllowancePage[];
  ownedOutputs?: OutputWithPreviousTransactionData[];
  broadcastResults?: Array<Record<string, unknown> | Error>;
  createAllowanceResults?: Array<Uint8Array[] | Error>;
} = {}): Promise<Fixture> {
  const signer = new UnsafeStatelessSparkSigner();
  await signer.createSparkWalletFromSeed(SPENDER_SEED, 0);
  const config = new WalletConfigService(
    { ...WalletConfig.LOCAL, network: "LOCAL" },
    signer,
  );

  const queryRequests: QueryAllowancesRequest[] = [];
  const createCallOptions: unknown[] = [];
  const broadcastCallOptions: unknown[] = [];
  const pages = [...allowancePages];
  const scriptedBroadcasts = [...broadcastResults];
  const scriptedCreates = [...createAllowanceResults];

  const tokenClient = {
    query_token_allowances: jest.fn(async (request: QueryAllowancesRequest) => {
      await Promise.resolve();
      queryRequests.push(request);
      const page = pages.shift();
      if (!page) {
        throw new Error("No scripted allowance page remaining");
      }
      return page;
    }),
    query_token_outputs: jest.fn(async () => {
      await Promise.resolve();
      return { outputsWithPreviousTransactionData: ownedOutputs };
    }),
    broadcast_transaction: jest.fn(
      async (_request: unknown, options: unknown) => {
        await Promise.resolve();
        broadcastCallOptions.push(options);
        const next = scriptedBroadcasts.shift();
        if (next === undefined) {
          throw new Error("No scripted broadcast result remaining");
        }
        if (next instanceof Error) {
          throw next;
        }
        return next;
      },
    ),
    create_token_allowance: jest.fn(
      async (_request: unknown, options: unknown) => {
        await Promise.resolve();
        createCallOptions.push(options);
        const next = scriptedCreates.shift();
        if (next === undefined) {
          throw new Error("No scripted create result remaining");
        }
        if (next instanceof Error) {
          throw next;
        }
        // Consensus creation returns the committed grant, not per-operator progress;
        // the scripted value only drives error injection.
        void next;
        return { allowance: undefined };
      },
    ),
  };

  const connectionManager = {
    createSparkTokenClient: jest.fn(() => Promise.resolve(tokenClient)),
    getCurrentServerTime: jest.fn(() => serverTime()),
  } as unknown as ConnectionManagerNodeJS;

  return {
    service: new TokenAllowanceService(config, connectionManager),
    queryRequests,
    createCallOptions,
    broadcastCallOptions,
  };
}

function revokedAllowanceInfo(): TokenAllowanceInfo {
  const payload = allowancePayload();
  const revokeTimestamp = 1747337990000;
  return {
    allowancePayload: payload,
    spentAmount: hexToBytes(ZERO_U128_HEX),
    status: TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_REVOKED,
    ownerSignature: signAsOwner(hashCreateTokenAllowancePayload(payload)),
    revokeSignature: signAsOwner(
      hashRevokeTokenAllowancePayload({
        version: 1,
        allowanceId: payload.allowanceId,
        ownerPublicKey: payload.ownerPublicKey,
        ownerProvidedTimestamp: revokeTimestamp,
      }),
    ),
    ownerProvidedRevokeTimestamp: revokeTimestamp,
    revokeVersion: 1,
  };
}

function signAsOwner(hash: Uint8Array): Uint8Array {
  return secp256k1.sign(hash, ownerPrivateKey).toDERRawBytes();
}

function allowancePayload(
  overrides: Partial<TokenAllowancePayload> = {},
): TokenAllowancePayload {
  return {
    version: 1,
    allowanceId: hexToBytes(ALLOWANCE_ID_HEX),
    ownerPublicKey: hexToBytes(OWNER_KEY_HEX),
    spenderPublicKey,
    tokenIdentifier: hexToBytes(TOKEN_ID_HEX),
    perTransactionCap: hexToBytes(PER_TX_CAP_HEX),
    totalLimit: hexToBytes(TOTAL_LIMIT_HEX),
    perTransactionUnlimited: false,
    totalUnlimited: false,
    recipientAllowlist: [],
    expiryTime: FUTURE_EXPIRY,
    network: 2,
    ownerProvidedTimestamp: 1747337980820,
    ...overrides,
  };
}

function allowanceInfo(
  overrides: Partial<TokenAllowanceInfo> = {},
): TokenAllowanceInfo {
  const payload = overrides.allowancePayload ?? allowancePayload();
  return {
    allowancePayload: payload,
    spentAmount: hexToBytes(ZERO_U128_HEX),
    status: TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_ACTIVE,
    ownerSignature: signAsOwner(hashCreateTokenAllowancePayload(payload)),
    revokeSignature: new Uint8Array(0),
    ownerProvidedRevokeTimestamp: 0,
    revokeVersion: 0,
    ...overrides,
  };
}

/** One page holding the allowance the pull tests spend against. */
function activeAllowancePage(
  payloadOverrides: Partial<TokenAllowancePayload> = {},
): AllowancePage[] {
  return [
    finalPage(
      allowanceInfo({ allowancePayload: allowancePayload(payloadOverrides) }),
    ),
  ];
}

function ownerOutput(
  id: string,
  tokenAmount: bigint,
  vout = 0,
): OutputWithPreviousTransactionData {
  return {
    output: {
      id,
      ownerPublicKey: hexToBytes(OWNER_KEY_HEX),
      tokenPublicKey: new Uint8Array(33).fill(2),
      tokenIdentifier: hexToBytes(TOKEN_ID_HEX),
      tokenAmount: numberToBytesBE(tokenAmount, 16),
      revocationCommitment: new Uint8Array(32).fill(3),
      status: TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE,
    },
    previousTransactionHash: new Uint8Array(32).fill(4),
    previousTransactionVout: vout,
  };
}

/** Minimal hashable response body for a committed broadcast. */
function finalTokenTransaction(): FinalTokenTransaction {
  return {
    version: 3,
    tokenTransactionMetadata: {
      network: 2,
      sparkOperatorIdentityPublicKeys: [],
      validityDurationSeconds: 180,
      clientCreatedTimestamp: new Date("2026-01-01T00:00:00.000Z"),
      invoiceAttachments: [],
    },
    tokenInputs: {
      $case: "transferInput",
      transferInput: { outputsToSpend: [] },
    },
    finalTokenOutputs: [],
  };
}

function pullParams(
  outputs: Array<{ receiverPublicKey: string; tokenAmount: bigint }>,
  selectedOutputs?: OutputWithPreviousTransactionData[],
) {
  return {
    allowanceId: ALLOWANCE_ID_HEX,
    ownerPublicKey: OWNER_KEY_HEX,
    tokenIdentifier: TOKEN_ID_HEX,
    outputs,
    selectedOutputs,
  };
}

describe("queryTokenAllowances pagination", () => {
  it("follows the response offset until the server reports no more pages", async () => {
    const lastRow = allowanceInfo({
      allowancePayload: allowancePayload({
        allowanceId: new Uint8Array(16).fill(2),
      }),
    });
    const { service, queryRequests } = await createService({
      allowancePages: [pageWithMore(PAGE_SIZE), finalPage(lastRow)],
    });

    const allowances = await service.queryTokenAllowances({
      ownerPublicKey: OWNER_KEY_HEX,
    });

    expect(allowances).toHaveLength(PAGE_SIZE + 1);
    expect(allowances.at(-1)).toEqual(lastRow);
    expect(queryRequests.map((request) => request.offset)).toEqual([
      0,
      PAGE_SIZE,
    ]);
    expect(queryRequests.every((request) => request.limit === PAGE_SIZE)).toBe(
      true,
    );
  });

  it("rejects a listed record whose owner proof does not verify", async () => {
    const forged = allowanceInfo();
    forged.ownerSignature = new Uint8Array(70).fill(1);
    const { service } = await createService({
      allowancePages: [finalPage(forged)],
    });

    await expect(
      service.queryTokenAllowances({
        ownerPublicKey: OWNER_KEY_HEX,
      }),
    ).rejects.toThrow("owner signature does not verify");
  });

  it("makes a single request when the first page is the last", async () => {
    const { service, queryRequests } = await createService({
      allowancePages: activeAllowancePage(),
    });

    await service.queryTokenAllowances({
      spenderPublicKey: spenderPublicKeyHex,
    });

    expect(queryRequests).toHaveLength(1);
  });

  it("stops instead of looping when the server repeats an offset", async () => {
    const { service, queryRequests } = await createService({
      allowancePages: [
        pageWithMore(PAGE_SIZE),
        pageWithMore(PAGE_SIZE),
        pageWithMore(PAGE_SIZE),
      ],
    });

    await service.queryTokenAllowances({
      ownerPublicKey: OWNER_KEY_HEX,
    });

    expect(queryRequests.map((request) => request.offset)).toEqual([
      0,
      PAGE_SIZE,
    ]);
  });

  it("stops at the aggregate page cap instead of accumulating forever", async () => {
    // A hostile or simply huge history can advance the offset indefinitely.
    const pages = Array.from({ length: 40 }, (_unused, i) =>
      pageWithMore(PAGE_SIZE * (i + 1)),
    );
    const { service, queryRequests } = await createService({
      allowancePages: pages,
    });

    await expect(
      service.queryTokenAllowances({
        ownerPublicKey: OWNER_KEY_HEX,
      }),
    ).rejects.toBeInstanceOf(SparkValidationError);
    expect(queryRequests.length).toBeLessThanOrEqual(
      ALLOWANCE_QUERY_MAX_PAGES + 1,
    );
  });

  it("forwards the filters on every page request", async () => {
    const { service, queryRequests } = await createService({
      allowancePages: [pageWithMore(PAGE_SIZE), finalPage(allowanceInfo())],
    });

    await service.queryTokenAllowances({
      ownerPublicKey: OWNER_KEY_HEX,
      tokenIdentifier: TOKEN_ID_HEX,
      includeInactive: true,
    });

    expect(queryRequests).toHaveLength(2);
    for (const request of queryRequests) {
      expect(request.ownerPublicKey).toEqual(hexToBytes(OWNER_KEY_HEX));
      expect(request.tokenIdentifier).toEqual(hexToBytes(TOKEN_ID_HEX));
      expect(request.includeInactive).toBe(true);
    }
  });
});

describe("createTokenAllowance validation", () => {
  async function rejectionFor(
    params: Parameters<TokenAllowanceService["createTokenAllowance"]>[0],
  ): Promise<Error> {
    const { service } = await createService();
    return await service.createTokenAllowance(params).then(
      () => {
        throw new Error("expected createTokenAllowance to reject");
      },
      (error: Error) => error,
    );
  }

  const boundedParams = {
    spenderPublicKey: RECIPIENT_HEX,
    tokenIdentifier: TOKEN_ID_HEX,
    perTransaction: { amount: 100n },
    total: { amount: 1000n },
    expiryTime: FUTURE_EXPIRY,
  };

  it("rejects a bounded per-transaction cap above the total limit", async () => {
    const error = await rejectionFor({
      ...boundedParams,
      perTransaction: { amount: 2000n },
    });

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(error.message).toContain("perTransaction.amount must not exceed");
  });

  // Pairing an amount with the unlimited flag, or omitting the amount without it, is no longer
  // representable: AllowanceCeiling is a discriminated union, so these are compile errors rather
  // than runtime rejections. @ts-expect-error is the assertion.
  it("makes contradictory ceilings a compile error", () => {
    // @ts-expect-error amount cannot accompany unlimited
    const withBoth: AllowanceCeiling = { unlimited: true, amount: 100n };
    // @ts-expect-error a bounded ceiling requires an amount
    const withNeither: AllowanceCeiling = { unlimited: false };
    expect(withBoth).toBeDefined();
    expect(withNeither).toBeDefined();
  });

  it("rejects a zero per-transaction cap", async () => {
    const error = await rejectionFor({
      ...boundedParams,
      perTransaction: { amount: 0n },
    });

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(error.message).toContain(
      "perTransaction.amount must be greater than 0",
    );
  });

  it("rejects a zero total", async () => {
    const error = await rejectionFor({
      ...boundedParams,
      total: { amount: 0n },
    });

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(error.message).toContain("total.amount must be greater than 0");
  });

  it("rejects a spender equal to the wallet identity key", async () => {
    const error = await rejectionFor({
      ...boundedParams,
      spenderPublicKey: spenderPublicKeyHex,
    });

    expect(error.message).toContain("spenderPublicKey must differ");
  });

  it("rejects an allowlist containing the owner key", async () => {
    const error = await rejectionFor({
      ...boundedParams,
      recipientAllowlist: [spenderPublicKeyHex],
    });

    expect(error.message).toContain(
      "recipientAllowlist must not contain the owner key",
    );
  });

  it("rejects an Invalid Date expiry instead of leaking a RangeError", async () => {
    const error = await rejectionFor({
      ...boundedParams,
      expiryTime: new Date("not a date"),
    });

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(error.message).toContain("expiryTime");
  });

  it("rejects an expiry whose whole second is not in the future", async () => {
    // The signed statement hashes expiry as Unix seconds and the SO validates
    // expiry truncated to whole seconds, so an expiry later in the current
    // second is already past once truncated.
    const now = Date.now();
    const error = await rejectionFor({
      ...boundedParams,
      expiryTime: new Date(now - (now % 1000) + 999),
    });

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(error.message).toContain("expiryTime must be in the future");
  });

  it("accepts an expiry a whole second into the future", async () => {
    const { service } = await createService({
      createAllowanceResults: [operatorPublicKeys],
    });
    const nextSecond = new Date((Math.floor(Date.now() / 1000) + 2) * 1000);

    const result = await service.createTokenAllowance({
      ...boundedParams,
      expiryTime: nextSecond,
    });

    expect(result.allowancePayload.expiryTime).toEqual(nextSecond);
  });

  it("refuses to sign when the coordinator clock is far ahead", async () => {
    const { service } = await createService({
      createAllowanceResults: [operatorPublicKeys],
      serverTime: () => new Date(Date.now() + 60 * 60 * 1000),
    });

    const error = await service.createTokenAllowance(boundedParams).then(
      () => {
        throw new Error("expected createTokenAllowance to reject");
      },
      (thrown: Error) => thrown,
    );

    expect(error).toBeInstanceOf(SparkValidationError);
    expect(error.message).toContain("diverges from local time");
  });

  it("refuses to sign when the coordinator clock is far behind", async () => {
    const { service } = await createService({
      createAllowanceResults: [operatorPublicKeys],
      serverTime: () => new Date(Date.now() - 60 * 60 * 1000),
    });

    await expect(
      service.createTokenAllowance(boundedParams),
    ).rejects.toBeInstanceOf(SparkValidationError);
  });

  it("signs with the coordinator sample when it agrees with local time", async () => {
    const agreeing = new Date(Date.now() + 5_000);
    const { service } = await createService({
      createAllowanceResults: [operatorPublicKeys],
      serverTime: () => agreeing,
    });

    const result = await service.createTokenAllowance(boundedParams);

    expect(result.allowancePayload.ownerProvidedTimestamp).toBe(
      agreeing.getTime(),
    );
  });

  it("emits the payload version every operator accepts", async () => {
    const { service } = await createService({
      createAllowanceResults: [operatorPublicKeys],
    });

    const result = await service.createTokenAllowance(boundedParams);

    expect(result.allowancePayload.version).toBe(1);
  });

  it("bounds the create RPC with a deadline and retry policy", async () => {
    const { service, createCallOptions } = await createService({
      createAllowanceResults: [operatorPublicKeys],
    });

    await service.createTokenAllowance(boundedParams);

    expect(createCallOptions).toHaveLength(1);
    expect(createCallOptions[0]).toMatchObject({
      retry: true,
      retryMaxAttempts: 3,
    });
    expect(
      (createCallOptions[0] as { deadline: Date }).deadline.getTime(),
    ).toBeGreaterThan(Date.now());
  });
});

describe("startAllowancePull", () => {
  it("appends owner change and signs every input with the allowance arm", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });

    const prepared = await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 400n }],
        [ownerOutput("out1", 1000n)],
      ),
    );

    const outputs = prepared.partialTokenTransaction.partialTokenOutputs;
    expect(outputs).toHaveLength(2);
    expect(outputs[1]!.ownerPublicKey).toEqual(hexToBytes(OWNER_KEY_HEX));
    expect(outputs[1]!.tokenAmount).toEqual(numberToBytesBE(600n, 16));
    expect(prepared.signatures).toHaveLength(1);
    expect(prepared.signatures[0]!.authoritySignatures?.$case).toBe(
      "allowanceSignature",
    );
    expect(prepared.allowanceId).toEqual(ALLOWANCE_ID_HEX);
  });

  it("never emits an output beyond the metered set", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });

    const prepared = await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 400n }],
        [ownerOutput("out1", 1000n)],
      ),
    );

    for (const output of prepared.partialTokenTransaction.partialTokenOutputs) {
      expect(bytesToHex(output.ownerPublicKey)).not.toBe(
        bytesToHex(spenderPublicKey),
      );
    }
  });

  it("attaches one allowance signature per selected input", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });

    const prepared = await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 900n }],
        [ownerOutput("out1", 500n, 0), ownerOutput("out2", 400n, 1)],
      ),
    );

    expect(prepared.signatures.map((sig) => sig.inputIndex)).toEqual([0, 1]);
  });

  it("selects the owner's outputs when the caller supplies none", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
      ownedOutputs: [ownerOutput("out1", 700n)],
    });

    const prepared = await service.startAllowancePull(
      pullParams([{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 700n }]),
    );

    expect(prepared.signatures).toHaveLength(1);
    expect(prepared.partialTokenTransaction.partialTokenOutputs).toHaveLength(
      1,
    );
  });

  it("rejects selected outputs that do not cover the pull amount", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 900n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("do not cover the pull amount");
  });

  it("rejects a selected output for a different token", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });
    const wrongToken = ownerOutput("out1", 100n);
    wrongToken.output!.tokenIdentifier = new Uint8Array(32).fill(7);

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [wrongToken],
        ),
      ),
    ).rejects.toThrow("does not match the allowance token");
  });

  it("rejects a selected output owned by someone else", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });
    const foreign = ownerOutput("out1", 100n);
    foreign.output!.ownerPublicKey = hexToBytes(RECIPIENT_HEX);

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [foreign],
        ),
      ),
    ).rejects.toThrow("not owned by the allowance owner");
  });

  it("rejects duplicate selected outputs", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });
    const dup = ownerOutput("out1", 100n);

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 200n }],
          [dup, ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("duplicate");
  });

  it("rejects an incomplete selected output", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage(),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [
            {
              previousTransactionHash: new Uint8Array(32),
              previousTransactionVout: 0,
            } as unknown as OutputWithPreviousTransactionData,
          ],
        ),
      ),
    ).rejects.toThrow("missing output data");
  });

  it("rejects a pull with no outputs", async () => {
    const { service } = await createService();

    await expect(service.startAllowancePull(pullParams([]))).rejects.toThrow(
      "No pull outputs provided",
    );
  });

  it("proceeds when the reported budget looks exhausted", async () => {
    // limit 100000, reserved 60000, pull 80000: the operators release the
    // stale reservation inside prepare and accept.
    const { service } = await createService({
      allowancePages: [
        finalPage(
          allowanceInfo({
            allowancePayload: allowancePayload({
              perTransactionUnlimited: true,
              perTransactionCap: new Uint8Array(16),
            }),
            spentAmount: numberToBytesBE(60000n, 16),
          }),
        ),
      ],
    });

    const prepared = await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 80000n }],
        [ownerOutput("out1", 80000n)],
      ),
    );

    expect(prepared.signatures).toHaveLength(1);
  });

  it("rejects a recipient outside the allowance allowlist", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage({
        recipientAllowlist: [new Uint8Array(33).fill(9)],
      }),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("not in the allowance recipient allowlist");
  });

  it("rejects a revoked allowance before building a transaction", async () => {
    const { service } = await createService({
      allowancePages: [finalPage(revokedAllowanceInfo())],
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("token allowance has been revoked");
  });

  it("rejects an allowance that has already expired", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage({
        expiryTime: new Date(Date.now() - 1000),
      }),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("token allowance has expired");
  });

  it("rejects a queried allowance whose owner does not match the request", async () => {
    // A hostile operator can sign a well-formed record for a DIFFERENT owner;
    // the proof verifies, so only the binding check catches the swap.
    const otherPriv = new Uint8Array(32).fill(5);
    const otherPayload = allowancePayload({
      ownerPublicKey: secp256k1.getPublicKey(otherPriv, true),
    });
    const { service } = await createService({
      allowancePages: [
        finalPage({
          allowancePayload: otherPayload,
          spentAmount: hexToBytes(ZERO_U128_HEX),
          status: TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_ACTIVE,
          ownerSignature: secp256k1
            .sign(hashCreateTokenAllowancePayload(otherPayload), otherPriv)
            .toDERRawBytes(),
          revokeSignature: new Uint8Array(0),
          ownerProvidedRevokeTimestamp: 0,
          revokeVersion: 0,
        }),
      ],
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("does not match");
  });

  it("rejects a queried allowance naming a different spender", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage({
        spenderPublicKey: hexToBytes(RECIPIENT_HEX),
      }),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("does not match");
  });

  it("rejects a queried allowance for a different token", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage({
        tokenIdentifier: new Uint8Array(32).fill(7),
      }),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("does not match");
  });

  it("rejects a queried allowance for a different network", async () => {
    const { service } = await createService({
      allowancePages: activeAllowancePage({ network: 1 }),
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("does not match");
  });

  it("resolves the active grant without scanning the tombstone history", async () => {
    const { service, queryRequests } = await createService({
      allowancePages: activeAllowancePage(),
    });

    await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
        [ownerOutput("out1", 100n)],
      ),
    );

    expect(queryRequests).toHaveLength(1);
    expect(queryRequests[0]!.includeInactive).toBe(false);
  });

  it("falls back to the inactive scan when no ACTIVE grant matches", async () => {
    const { service, queryRequests } = await createService({
      allowancePages: [
        finalPage(),
        finalPage(
          allowanceInfo({
            status: TokenAllowanceStatus.TOKEN_ALLOWANCE_STATUS_EXHAUSTED,
          }),
        ),
      ],
    });

    const prepared = await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
        [ownerOutput("out1", 100n)],
      ),
    );

    expect(prepared.signatures).toHaveLength(1);
    expect(queryRequests.map((r) => r.includeInactive)).toEqual([false, true]);
  });

  it("rejects an allowance whose id is on no page", async () => {
    const { service } = await createService({
      allowancePages: [
        finalPage(
          allowanceInfo({
            allowancePayload: allowancePayload({
              allowanceId: new Uint8Array(16).fill(8),
            }),
          }),
        ),
        finalPage(
          allowanceInfo({
            allowancePayload: allowancePayload({
              allowanceId: new Uint8Array(16).fill(8),
            }),
          }),
        ),
      ],
    });

    await expect(
      service.startAllowancePull(
        pullParams(
          [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
          [ownerOutput("out1", 100n)],
        ),
      ),
    ).rejects.toThrow("token allowance not found");
  });

  it("finds an allowance that only appears on a later page", async () => {
    const { service } = await createService({
      allowancePages: [pageWithMore(PAGE_SIZE), finalPage(allowanceInfo())],
    });

    const prepared = await service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
        [ownerOutput("out1", 100n)],
      ),
    );

    expect(prepared.allowanceId).toEqual(ALLOWANCE_ID_HEX);
  });
});

describe("commitAllowancePull", () => {
  async function preparedPull(
    broadcastResults: Array<Record<string, unknown> | Error>,
  ): Promise<
    Fixture & {
      prepared: Awaited<
        ReturnType<TokenAllowanceService["startAllowancePull"]>
      >;
    }
  > {
    const fixture = await createService({
      allowancePages: activeAllowancePage(),
      broadcastResults,
    });
    const prepared = await fixture.service.startAllowancePull(
      pullParams(
        [{ receiverPublicKey: RECIPIENT_HEX, tokenAmount: 100n }],
        [ownerOutput("out1", 100n)],
      ),
    );
    return { ...fixture, prepared };
  }

  it("broadcasts under the shared token-transaction call policy", async () => {
    const { service, prepared, broadcastCallOptions } = await preparedPull([
      { finalTokenTransaction: finalTokenTransaction() },
    ]);

    await service.commitAllowancePull(prepared);

    const { deadline, ...policy } = broadcastCallOptions[0] as {
      deadline: Date;
    };
    const { deadline: expectedDeadline, ...expectedPolicy } =
      tokenTransactionCallOptions() as unknown as { deadline: Date };
    expect(policy).toEqual(expectedPolicy);
    expect(deadline.getTime()).toBeGreaterThan(Date.now());
    expect(expectedDeadline).toBeInstanceOf(Date);
  });

  it("rejects a broadcast response missing the final transaction", async () => {
    const { service, prepared } = await preparedPull([{}]);

    await expect(service.commitAllowancePull(prepared)).rejects.toThrow(
      "Final token transaction missing",
    );
  });
});
