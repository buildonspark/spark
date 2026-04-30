import { LoggingLevel } from "@lightsparkdev/core";
import { WalletConfigService } from "../services/config.js";
import {
  getDefaultUseTokenPrimitivesBindings,
  LOG_SERVICE_NAMES,
  WalletConfig,
  type ConfigOptions,
  type LogServicesOptions,
} from "../services/wallet-config.js";
import type { SparkSigner } from "../signer/signer.js";

const mockSigner = {} as SparkSigner;

const createConfigService = (logOptions?: ConfigOptions["log"]) => {
  const options =
    logOptions === undefined
      ? ({} as ConfigOptions)
      : ({ log: logOptions } as ConfigOptions);
  return new WalletConfigService(options, mockSigner);
};

const DEFAULT_METHOD_LOGGING_SERVICES = [
  "sparkWallet",
  "sparkReadonlyClient",
  "connectionManager",
  "sspClient",
  "transferService",
  "lightningService",
  "depositService",
  "tokenTransactionService",
] as const;

describe("wallet config", () => {
  it("defaults to token primitive bindings outside React Native", () => {
    expect(getDefaultUseTokenPrimitivesBindings(false)).toBe(true);
    expect(WalletConfig.REGTEST.useTokenPrimitivesBindings).toBe(true);
  });

  it("keeps token primitive bindings disabled by default in React Native", () => {
    expect(getDefaultUseTokenPrimitivesBindings(true)).toBe(false);
  });
});

describe("WalletConfigService logging normalization", () => {
  it("enables warn service logs when logging is not configured", () => {
    const service = createConfigService();

    expect(service.getLog()).toBe(true);
    expect(service.getLoggingLevel()).toBe("WARN");
    expect(service.getLoggingConfig()).toEqual({
      level: "WARN",
      timestamps: true,
      services: Object.fromEntries(
        LOG_SERVICE_NAMES.map((serviceName) => [
          serviceName,
          {
            enabled: true,
            level: "WARN",
            methods: {
              enabled: false,
              collapseConsecutive: true,
              excludedMethods: [],
              exitOnly: true,
            },
          },
        ]),
      ),
    });
  });

  it("treats explicit false log config as disabled", () => {
    const service = createConfigService(false);

    expect(service.getLog()).toBe(false);
    expect(service.getLoggingLevel()).toBe("WARN");
    for (const serviceName of LOG_SERVICE_NAMES) {
      expect(service.getLoggingConfig().services[serviceName]).toMatchObject({
        enabled: false,
        methods: { enabled: false },
      });
    }
  });

  it("treats explicit undefined log config as disabled", () => {
    const service = new WalletConfigService(
      { log: undefined } as ConfigOptions,
      mockSigner,
    );

    expect(service.getLog()).toBe(false);
    expect(service.getLoggingLevel()).toBe("WARN");
  });

  it("enables all service logs when log is true", () => {
    const service = createConfigService(true);
    const services = service.getLoggingConfig().services;

    expect(service.getLog()).toBe(true);
    expect(service.getLoggingLevel()).toBe("TRACE");
    for (const serviceName of LOG_SERVICE_NAMES) {
      expect(services[serviceName]).toMatchObject({
        enabled: true,
        level: "TRACE",
        methods: {
          enabled: DEFAULT_METHOD_LOGGING_SERVICES.includes(
            serviceName as (typeof DEFAULT_METHOD_LOGGING_SERVICES)[number],
          ),
        },
      });
    }
  });

  it("uses trace defaults when only level trace is provided", () => {
    const service = createConfigService({ level: "TRACE" });

    expect(service.getLoggingLevel()).toBe("TRACE");
    for (const serviceName of LOG_SERVICE_NAMES) {
      expect(service.getLoggingConfig().services[serviceName]).toMatchObject({
        enabled: true,
        methods: {
          enabled: DEFAULT_METHOD_LOGGING_SERVICES.includes(
            serviceName as (typeof DEFAULT_METHOD_LOGGING_SERVICES)[number],
          ),
        },
      });
    }
  });

  it("treats level names as case-insensitive for trace defaults", () => {
    const service = createConfigService({ level: "trace" });
    const services = service.getLoggingConfig().services;

    expect(service.getLoggingLevel()).toBe("trace");
    expect(services.sparkWallet).toMatchObject({
      level: "trace",
      methods: { enabled: true },
    });
    expect(services.sspClient).toMatchObject({
      level: "trace",
      methods: { enabled: true },
    });
    expect(services.sparkReadonlyClient).toMatchObject({
      level: "trace",
      methods: { enabled: true },
    });
    expect(services.coopExitService).toMatchObject({
      level: "trace",
      methods: { enabled: false },
    });
    expect(services.swapService).toMatchObject({
      level: "trace",
      methods: { enabled: false },
    });
    expect(services.leafManager).toMatchObject({
      level: "trace",
      methods: { enabled: false },
    });
  });

  it("allows passing LoggingLevel enum values", () => {
    const service = createConfigService({ level: LoggingLevel.Trace });

    expect(service.getLoggingLevel()).toBe(LoggingLevel.Trace);
    expect(service.getLoggingConfig().services.sparkWallet).toMatchObject({
      level: LoggingLevel.Trace,
      methods: { enabled: true },
    });
  });

  it("respects non-trace level without enabling method logging", () => {
    const service = createConfigService({ level: "INFO" });

    expect(service.getLoggingLevel()).toBe("INFO");
    expect(service.getLoggingConfig().services.sparkWallet).toMatchObject({
      enabled: true,
      level: "INFO",
      methods: { enabled: false },
    });
    expect(service.getLoggingConfig().services.connectionManager).toMatchObject(
      {
        enabled: true,
        level: "INFO",
        methods: { enabled: false },
      },
    );
  });

  it("honors explicit service overrides", () => {
    const service = createConfigService({
      services: {
        sparkWallet: { methods: { enabled: true, exitOnly: false } },
        connectionManager: { methods: { collapseConsecutive: false } },
        transferService: true,
        sspClient: false,
        coopExitService: { methods: true },
        swapService: { methods: true },
        leafManager: { methods: true },
      },
    });
    const services = service.getLoggingConfig().services;

    expect(service.getLoggingLevel()).toBe("WARN");
    expect(services.sparkWallet).toMatchObject({
      enabled: true,
      methods: { enabled: true, exitOnly: false },
    });
    expect(services.connectionManager).toMatchObject({
      enabled: true,
      methods: { enabled: false, collapseConsecutive: false },
    });
    expect(services.transferService).toMatchObject({
      enabled: true,
      methods: { enabled: true },
    });
    expect(services.sspClient).toMatchObject({
      enabled: false,
      methods: { enabled: false },
    });
    expect(services.coopExitService).toMatchObject({
      enabled: true,
      methods: { enabled: true },
    });
    expect(services.swapService).toMatchObject({
      enabled: true,
      methods: { enabled: true },
    });
    expect(services.leafManager).toMatchObject({
      enabled: true,
      methods: { enabled: true },
    });
  });

  it("allows every logging service to opt into method logging", () => {
    const service = createConfigService({
      services: Object.fromEntries(
        LOG_SERVICE_NAMES.map((serviceName) => [
          serviceName,
          { methods: true },
        ]),
      ),
    });

    for (const serviceName of LOG_SERVICE_NAMES) {
      expect(service.getLoggingConfig().services[serviceName]).toMatchObject({
        enabled: true,
        methods: { enabled: true },
      });
    }
  });

  it("supports services all shorthand for method logging on every service", () => {
    const service = createConfigService({
      services: "all",
    });

    expect(service.getLog()).toBe(true);
    for (const serviceName of LOG_SERVICE_NAMES) {
      expect(service.getLoggingConfig().services[serviceName]).toMatchObject({
        enabled: true,
        methods: { enabled: true },
      });
    }
  });

  it("allows overriding timestamps", () => {
    const service = createConfigService({ timestamps: false });

    expect(service.getLoggingConfig()).toMatchObject({
      level: "WARN",
      timestamps: false,
    });
    expect(service.getLog()).toBe(true);
    expect(service.getLoggingConfig().services.sparkWallet).toMatchObject({
      enabled: true,
      level: "WARN",
      methods: { enabled: false },
    });
  });

  it("ignores undefined and null service entries", () => {
    const service = new WalletConfigService(
      {
        log: {
          services: {
            sparkWallet: undefined,
            connectionManager: null,
          } as unknown as LogServicesOptions,
        },
      } as ConfigOptions,
      mockSigner,
    );

    expect(service.getLoggingConfig().services.sparkWallet).toMatchObject({
      enabled: true,
      level: "WARN",
      methods: { enabled: false },
    });
    expect(service.getLoggingConfig().services.connectionManager).toMatchObject(
      {
        enabled: true,
        level: "WARN",
        methods: { enabled: false },
      },
    );
  });
});
