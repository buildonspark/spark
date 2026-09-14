import { Requester } from "@lightsparkdev/core";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  jest,
} from "@jest/globals";
import { type WalletConfigService } from "../services/config.js";
import { type ConnectionManager } from "../services/connection/connection.js";
import { SparkValidationError } from "../errors/index.js";
import { SparkWallet } from "../spark-wallet/spark-wallet.js";

type InitWalletInternals = {
  createClientsAndSyncWallet: () => Promise<void>;
  leafManager: { swapService: unknown };
  logger: { context: string };
  sspClient: { logger: { context: string } };
  swapService: { sspClient: unknown };
  syncWallet: () => Promise<void>;
};

function initWalletInternals(wallet: SparkWallet): InitWalletInternals {
  return wallet as unknown as InitWalletInternals;
}

class InitServiceRefreshTestWallet extends SparkWallet {
  constructor(
    private readonly connectionManagerStub: {
      createClients: ReturnType<typeof jest.fn>;
    },
  ) {
    super({
      network: "LOCAL",
    });
    this.connectionManager =
      connectionManagerStub as unknown as ConnectionManager;
    initWalletInternals(this).syncWallet = jest.fn(async () => {
      await Promise.resolve();
    });
  }

  protected override buildConnectionManager(_config: WalletConfigService) {
    return {
      createClients: async () => {
        await Promise.resolve();
      },
      closeConnections: async () => {
        await Promise.resolve();
      },
      subscribeToEvents: async function* () {},
      getSessionId: () => "test-session",
    } as unknown as ConnectionManager;
  }

  protected override async setupBackgroundStream() {
    await Promise.resolve();
    return;
  }

  public async initializeSignerForTest(): Promise<void> {
    await this.config.signer.createSparkWalletFromSeed(
      new Uint8Array(32).fill(1),
      0,
    );
  }
}

describe("SparkWallet initialization", () => {
  beforeEach(() => {
    jest
      .spyOn(
        Requester.prototype as unknown as { initWsClient: () => Promise<null> },
        "initWsClient",
      )
      .mockResolvedValue(null);
  });

  afterEach(() => {
    jest.restoreAllMocks();
  });

  it("keeps SSP-backed services stable while updating logger context", async () => {
    const connectionManagerStub = {
      createClients: jest.fn(async () => {
        await Promise.resolve();
      }),
    };
    const wallet = new InitServiceRefreshTestWallet(connectionManagerStub);
    await wallet.initializeSignerForTest();

    const internalWallet = initWalletInternals(wallet);
    const originalSspClient = internalWallet.sspClient;
    const originalSwapService = internalWallet.swapService;
    const originalLeafManager = internalWallet.leafManager;
    const originalSspLogger = originalSspClient.logger;
    const originalSspLoggerContext = originalSspLogger.context;

    await internalWallet.createClientsAndSyncWallet();

    expect(connectionManagerStub.createClients).toHaveBeenCalledTimes(1);
    expect(internalWallet.sspClient).toBe(originalSspClient);
    expect(internalWallet.swapService).toBe(originalSwapService);
    expect(internalWallet.leafManager).toBe(originalLeafManager);
    expect(internalWallet.sspClient.logger).toBe(originalSspLogger);
    expect(originalSspLogger.context).not.toBe(originalSspLoggerContext);
    expect(originalSspLogger.context).toMatch(/^SspClient:[0-9a-f]{8}#\d+$/);
    expect(internalWallet.swapService.sspClient).toBe(internalWallet.sspClient);
    expect(internalWallet.leafManager.swapService).toBe(
      internalWallet.swapService,
    );
  });
});

describe("SparkWallet seed and mnemonic resolution", () => {
  type SeedResolverInternals = {
    resolveSeedAndMnemonic: (
      mnemonicOrSeed?: string,
      accountNumber?: number,
    ) => Promise<{ seed: Uint8Array; mnemonic: string | undefined }>;
  };

  const resolveSeedAndMnemonic = (wallet: SparkWallet) =>
    wallet as unknown as SeedResolverInternals;

  it("rejects mnemonic-shaped strings that fail BIP-39 checksum validation", async () => {
    // Twelve valid wordlist words with an invalid checksum.
    const invalidMnemonic = Array(12).fill("abandon").join(" ");
    const wallet = new InitServiceRefreshTestWallet({
      createClients: jest.fn(async () => {}),
    });

    await expect(
      resolveSeedAndMnemonic(wallet).resolveSeedAndMnemonic(invalidMnemonic),
    ).rejects.toBeInstanceOf(SparkValidationError);
    await expect(
      resolveSeedAndMnemonic(wallet).resolveSeedAndMnemonic(invalidMnemonic),
    ).rejects.toThrow(/checksum/i);
  });

  it("still resolves hex seed strings", async () => {
    const wallet = new InitServiceRefreshTestWallet({
      createClients: jest.fn(async () => {}),
    });

    const { seed, mnemonic } = await resolveSeedAndMnemonic(
      wallet,
    ).resolveSeedAndMnemonic("deadbeef".repeat(8));

    expect(mnemonic).toBeUndefined();
    expect(seed).toHaveLength(32);
  });

  it("resolves valid mnemonics via the mnemonic path", async () => {
    const wallet = new InitServiceRefreshTestWallet({
      createClients: jest.fn(async () => {}),
    });
    await wallet.initializeSignerForTest();
    const validMnemonic =
      "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";

    const { seed, mnemonic } =
      await resolveSeedAndMnemonic(wallet).resolveSeedAndMnemonic(
        validMnemonic,
      );

    expect(mnemonic).toBe(validMnemonic);
    expect(seed).toHaveLength(64);
  });
});
