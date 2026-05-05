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
import { SparkWallet } from "../spark-wallet/spark-wallet.js";

class InitServiceRefreshTestWallet extends SparkWallet {
  constructor(
    private readonly connectionManagerStub: {
      createClients: ReturnType<typeof jest.fn>;
    },
  ) {
    super({
      network: "LOCAL",
    });
    this.connectionManager = connectionManagerStub as any;
    (this as any).syncWallet = jest.fn(async () => {});
  }

  protected override buildConnectionManager(_config: WalletConfigService) {
    return {
      createClients: async () => {},
      closeConnections: async () => {},
      subscribeToEvents: async function* () {},
      getSessionId: () => "test-session",
    } as any;
  }

  protected override async setupBackgroundStream() {
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
      .spyOn(Requester.prototype as any, "initWsClient")
      .mockResolvedValue(null);
  });

  afterEach(() => {
    jest.restoreAllMocks();
  });

  it("keeps SSP-backed services stable while updating logger context", async () => {
    const connectionManagerStub = {
      createClients: jest.fn(async () => {}),
    };
    const wallet = new InitServiceRefreshTestWallet(connectionManagerStub);
    await wallet.initializeSignerForTest();

    const internalWallet = wallet as any;
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
    expect(originalSspLogger.context).toMatch(/^SspClient:[0-9a-f]{8}$/);
    expect(internalWallet.swapService.sspClient).toBe(internalWallet.sspClient);
    expect(internalWallet.leafManager.swapService).toBe(
      internalWallet.swapService,
    );
  });
});
