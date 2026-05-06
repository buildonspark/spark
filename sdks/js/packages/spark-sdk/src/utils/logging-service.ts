import { LoggingLevel, type Logger } from "@lightsparkdev/core";
import type {
  LogConfig,
  LogServiceName,
  ServiceLoggingConfig,
} from "../services/wallet-config.js";
import { LOG_SERVICE_NAMES } from "../services/wallet-config.js";
import type { WalletConfigService } from "../services/config.js";
import { createLogFileWriter, type LogFileWriter } from "./log-file-writer.js";
import { MethodCallLogger } from "./method-logger.js";
import { createSdkLogger } from "./sdk-logger.js";

type WrappedMethod = (...args: unknown[]) => unknown;

export type ServiceMethodDecorator = (
  methodName: string,
  method: WrappedMethod,
  receiver: unknown,
) => WrappedMethod;

type WrapMethodOnTargetOptions = {
  decorator?: ServiceMethodDecorator;
  originalMethod?: WrappedMethod;
  receiver?: unknown;
  errorMessage?: string;
};

type WrapNamedMethodsOptions = {
  decorator?: ServiceMethodDecorator;
  errorMessage?: (methodName: string) => string;
};

type WrapPrototypeMethodsOptions = {
  decorator?: ServiceMethodDecorator;
  excludeMethods?: readonly string[];
  startAtPrototype?: object | null;
  stopAtPrototype?: object | null;
};

type ServiceLoggingState = {
  config: ServiceLoggingConfig;
  logger: Logger;
  loggerName: string;
  methodCallLogger: MethodCallLogger;
};

const SERVICE_LOGGER_NAMES = {
  sparkWallet: "SparkWallet",
  sparkReadonlyClient: "SparkReadonlyClient",
  connectionManager: "ConnectionManager",
  serverTimeSync: "ServerTimeSync",
  sspClient: "SspClient",
  sparkAuthProvider: "SparkAuthProvider",
  signingService: "SigningService",
  transferService: "TransferService",
  lightningService: "LightningService",
  depositService: "DepositService",
  tokenTransactionService: "TokenTransactionService",
  tokenOutputManager: "TokenOutputManager",
  coopExitService: "CoopExitService",
  swapService: "SwapService",
  leafManager: "LeafManager",
  bareHttpTransport: "BareHttpTransport",
  xhrTransport: "XHRTransport",
} as const satisfies Record<LogServiceName, string>;

export type LogServiceDisplayName =
  (typeof SERVICE_LOGGER_NAMES)[LogServiceName];

const SERVICE_NAMES_BY_LOGGER_NAME = Object.fromEntries(
  Object.entries(SERVICE_LOGGER_NAMES).map(([serviceName, loggerName]) => [
    loggerName,
    serviceName,
  ]),
) as Record<LogServiceDisplayName, LogServiceName>;

let loggingServiceInstanceCounter = 0;
const LOG_FILE_CLOSE_TIMEOUT_MS = 1_500;

function settleFileWriterClose(
  fileWriter: LogFileWriter | undefined,
): Promise<void> {
  return Promise.resolve()
    .then(() => fileWriter?.close?.())
    .then(
      () => undefined,
      () => undefined,
    );
}

function withTimeout(promise: Promise<void>, timeoutMs: number): Promise<void> {
  let timeoutId: ReturnType<typeof setTimeout> | undefined;
  const timeoutPromise = new Promise<void>((resolve) => {
    timeoutId = setTimeout(resolve, timeoutMs);
  });

  return Promise.race([promise, timeoutPromise]).finally(() => {
    if (timeoutId !== undefined) {
      clearTimeout(timeoutId);
    }
  });
}

function disabledServiceConfig(): ServiceLoggingConfig {
  return {
    enabled: false,
    level: "WARN",
    methods: {
      enabled: false,
      collapseConsecutive: true,
      excludedMethods: [],
      exitOnly: true,
    },
  };
}

function disabledLogConfig(): LogConfig {
  return {
    level: "WARN",
    timestamps: true,
    console: true,
    services: Object.fromEntries(
      LOG_SERVICE_NAMES.map((serviceName) => [
        serviceName,
        disabledServiceConfig(),
      ]),
    ) as Record<LogServiceName, ServiceLoggingConfig>,
  };
}

export class LoggingService {
  private readonly config: LogConfig;
  private readonly fileWriter?: LogFileWriter;
  private readonly states = new Map<LogServiceName, ServiceLoggingState>();
  private readonly instanceId: number;
  private closePromise?: Promise<void>;
  private instanceSuffix?: string;

  static fromConfig(config: WalletConfigService): LoggingService {
    if (typeof config.getLoggingConfig !== "function") {
      return LoggingService.disabled();
    }

    return new LoggingService(config.getLoggingConfig());
  }

  static disabled(): LoggingService {
    return new LoggingService(disabledLogConfig());
  }

  constructor(config: LogConfig) {
    this.config = config;
    this.fileWriter = createLogFileWriter(config.file);
    loggingServiceInstanceCounter += 1;
    this.instanceId = loggingServiceInstanceCounter;
  }

  public logger(serviceName: LogServiceDisplayName): Logger {
    return this.getState(this.resolveServiceName(serviceName)).logger;
  }

  public wrap(
    serviceName: LogServiceDisplayName,
    methodName: string,
    method: WrappedMethod,
    receiver: unknown,
    decorator?: ServiceMethodDecorator,
  ): WrappedMethod {
    const state = this.getState(this.resolveServiceName(serviceName));
    const decoratedMethod = decorator
      ? decorator(methodName, method, receiver)
      : method;
    return state.methodCallLogger.wrap(methodName, decoratedMethod, receiver);
  }

  public rename(serviceName: LogServiceDisplayName, loggerName: string) {
    const resolvedServiceName = this.resolveServiceName(serviceName);
    const state = this.getState(resolvedServiceName);
    if (state.loggerName === loggerName) {
      return;
    }

    this.configure(resolvedServiceName, loggerName);
  }

  public setInstanceSuffix(suffix: string) {
    if (this.instanceSuffix === suffix) {
      return;
    }

    this.instanceSuffix = suffix;
    for (const serviceName of this.states.keys()) {
      this.configure(serviceName, this.getLoggerName(serviceName));
    }
  }

  public flushPendingLogs(serviceName: LogServiceDisplayName) {
    this.getState(
      this.resolveServiceName(serviceName),
    ).methodCallLogger.flushPendingLogs();
  }

  public async close() {
    for (const state of this.states.values()) {
      state.methodCallLogger.flushPendingLogs();
    }

    this.closePromise ??= withTimeout(
      settleFileWriterClose(this.fileWriter),
      LOG_FILE_CLOSE_TIMEOUT_MS,
    );
    await this.closePromise;
  }

  public setMethodLoggingEnabled(
    serviceName: LogServiceDisplayName,
    enabled: boolean,
  ) {
    const resolvedServiceName = this.resolveServiceName(serviceName);
    const state = this.getState(resolvedServiceName);
    if (state.methodCallLogger.isEnabled() === enabled) {
      return;
    }

    if (!enabled) {
      this.flushPendingLogs(serviceName);
    }

    state.methodCallLogger.setEnabled(enabled);
    this.configure(resolvedServiceName);
  }

  public isMethodLoggingEnabled(serviceName: LogServiceDisplayName) {
    return this.getState(
      this.resolveServiceName(serviceName),
    ).methodCallLogger.isEnabled();
  }

  public wrapMethodOnTarget<T extends object>(
    serviceName: LogServiceDisplayName,
    target: T,
    methodName: string,
    options?: WrapMethodOnTargetOptions,
  ) {
    const originalMethod =
      options?.originalMethod ??
      ((target as Record<string, unknown>)[methodName] as WrappedMethod);

    if (typeof originalMethod !== "function") {
      throw new Error(
        options?.errorMessage ?? `Method ${methodName} is not a function.`,
      );
    }

    const receiver = options?.receiver ?? target;
    const wrappedMethod = this.wrap(
      serviceName,
      methodName,
      originalMethod,
      receiver,
      options?.decorator,
    );

    (target as Record<string, WrappedMethod>)[methodName] = wrappedMethod;
    return wrappedMethod;
  }

  public wrapNamedMethods<T extends object>(
    serviceName: LogServiceDisplayName,
    target: T,
    methodNames: readonly string[],
    options?: WrapNamedMethodsOptions,
  ) {
    methodNames.forEach((methodName) =>
      this.wrapMethodOnTarget(serviceName, target, methodName, {
        decorator: options?.decorator,
        errorMessage: options?.errorMessage?.(methodName),
      }),
    );
  }

  public wrapPrototypeMethods(
    serviceName: LogServiceDisplayName,
    target: object,
    options?: WrapPrototypeMethodsOptions,
  ) {
    const resolvedServiceName = this.resolveServiceName(serviceName);
    const shouldWrap =
      options?.decorator !== undefined ||
      this.getState(resolvedServiceName).methodCallLogger.isEnabled();
    if (!shouldWrap) {
      return;
    }

    const seen = new Set<string>();
    const excludedMethods = new Set(options?.excludeMethods ?? []);
    const stopAtPrototype = options?.stopAtPrototype ?? Object.prototype;
    let proto: object | null =
      options?.startAtPrototype ?? Reflect.getPrototypeOf(target);

    while (proto && proto !== stopAtPrototype) {
      for (const methodName of Object.getOwnPropertyNames(proto)) {
        if (methodName === "constructor") {
          continue;
        }

        if (seen.has(methodName) || excludedMethods.has(methodName)) {
          continue;
        }

        const descriptor = Object.getOwnPropertyDescriptor(proto, methodName);
        if (!descriptor || typeof descriptor.value !== "function") {
          continue;
        }

        seen.add(methodName);
        this.wrapMethodOnTarget(serviceName, target, methodName, {
          decorator: options?.decorator,
          originalMethod: descriptor.value as WrappedMethod,
        });
      }

      proto = Reflect.getPrototypeOf(proto);
    }
  }

  private getState(serviceName: LogServiceName): ServiceLoggingState {
    const existingState = this.states.get(serviceName);
    if (existingState) {
      return existingState;
    }

    const config = this.config.services[serviceName];
    const loggerName = this.getLoggerName(serviceName);
    const logger = createSdkLogger(
      loggerName,
      {
        enabled: false,
        level: config.level,
        timestamps: this.config.timestamps,
      },
      {
        console: this.config.console,
        fileWriter: this.fileWriter,
      },
    );
    const state: ServiceLoggingState = {
      config,
      logger,
      loggerName,
      methodCallLogger: new MethodCallLogger(logger, config.methods),
    };
    this.states.set(serviceName, state);
    this.configure(serviceName);
    return state;
  }

  private configure(serviceName: LogServiceName, loggerName?: string) {
    const state = this.getState(serviceName);
    const nextLoggerName = loggerName ?? state.loggerName;
    const shouldUseLogger =
      state.config.enabled || state.methodCallLogger.isEnabled();
    const level = state.methodCallLogger.isEnabled()
      ? LoggingLevel.Trace
      : state.config.level;

    state.logger.context = nextLoggerName;
    state.logger.setOptions({
      enabled: shouldUseLogger,
      level,
      timestamps: this.config.timestamps,
    });
    state.loggerName = nextLoggerName;
  }

  private getLoggerName(serviceName: LogServiceName): string {
    const serviceDisplayName = SERVICE_LOGGER_NAMES[serviceName];
    const suffix = this.instanceSuffix ?? String(this.instanceId);
    return `${serviceDisplayName}:${suffix}`;
  }

  private resolveServiceName(
    serviceName: LogServiceDisplayName,
  ): LogServiceName {
    const resolvedServiceName = SERVICE_NAMES_BY_LOGGER_NAME[serviceName];
    if (!resolvedServiceName) {
      throw new Error(`Unknown logging service: ${serviceName}`);
    }
    return resolvedServiceName;
  }
}

export { LOG_SERVICE_NAMES };
