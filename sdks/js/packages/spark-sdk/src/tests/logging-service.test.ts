import { describe, expect, it } from "@jest/globals";
import { LoggingLevel, type Logger } from "@lightsparkdev/core";
import {
  LoggingService,
  type ServiceMethodDecorator,
} from "../utils/logging-service.js";
import { MethodCallLogger } from "../utils/method-logger.js";
import {
  LOG_SERVICE_NAMES,
  type LogConfig,
  type LogServiceName,
  type ServiceLoggingConfig,
} from "../services/wallet-config.js";

function serviceConfig(
  overrides?: Partial<ServiceLoggingConfig>,
): ServiceLoggingConfig {
  return {
    enabled: false,
    level: "WARN",
    methods: {
      enabled: false,
      collapseConsecutive: true,
      excludedMethods: [],
      exitOnly: true,
    },
    ...overrides,
  };
}

function createLogConfig(
  serviceName: LogServiceName,
  overrides?: Partial<ServiceLoggingConfig>,
): LogConfig {
  return {
    level: "WARN",
    timestamps: true,
    services: Object.fromEntries(
      LOG_SERVICE_NAMES.map((name) => [
        name,
        serviceConfig(name === serviceName ? overrides : undefined),
      ]),
    ) as Record<LogServiceName, ServiceLoggingConfig>,
  };
}

function createLoggingService(overrides?: { loggingEnabled?: boolean }) {
  const suffix = Math.random().toString(16).slice(2);
  const logging = new LoggingService(
    createLogConfig("sparkWallet", {
      enabled: overrides?.loggingEnabled ?? false,
    }),
  );
  logging.setInstanceSuffix(suffix);
  return logging;
}

const decorateResult: ServiceMethodDecorator =
  (methodName, originalMethod, receiver) =>
  (...args: unknown[]) =>
    `[${methodName}] ${String(originalMethod.apply(receiver, args))}`;

class WrappedTargetBase {
  public baseMethod(value: string) {
    return `base:${value}`;
  }
}

class WrappedTarget extends WrappedTargetBase {
  public ownMethod(value: string) {
    return this.baseMethod(`own:${value}`);
  }

  public skippedMethod() {
    return "skipped";
  }

  public async *streamValues() {
    yield "one";
    yield "two";
  }
}

describe("LoggingService", () => {
  it("keeps the service logger stable while enabling method logging on demand", () => {
    const logging = createLoggingService();
    const logger = logging.logger("SparkWallet");

    expect(logger.options.enabled).toBe(false);

    logging.setMethodLoggingEnabled("SparkWallet", true);

    expect(logging.isMethodLoggingEnabled("SparkWallet")).toBe(true);
    expect(logging.logger("SparkWallet")).toBe(logger);
    expect(logger.options.enabled).toBe(true);
    expect(logger.options.level).toBe(LoggingLevel.Trace);
  });

  it("renames an enabled logger in place", () => {
    const logging = createLoggingService({ loggingEnabled: true });
    const initialLogger = logging.logger("SparkWallet");
    const loggerName = `SparkWallet:${Math.random().toString(16)}`;

    logging.rename("SparkWallet", loggerName);

    expect(logging.logger("SparkWallet")).toBe(initialLogger);
    expect(initialLogger.context).toBe(loggerName);
    expect(initialLogger.options.enabled).toBe(true);
  });

  it("wraps named methods on a target", () => {
    const logging = createLoggingService();
    const target = new WrappedTarget();

    logging.wrapNamedMethods("SparkWallet", target, ["ownMethod"], {
      decorator: decorateResult,
    });

    expect(target.ownMethod("value")).toBe("[ownMethod] base:own:value");
    expect(target.baseMethod("value")).toBe("base:value");
  });

  it("wraps prototype methods while respecting exclusions", () => {
    const logging = createLoggingService();
    const target = new WrappedTarget();

    logging.wrapPrototypeMethods("SparkWallet", target, {
      decorator: decorateResult,
      excludeMethods: ["skippedMethod"],
    });

    expect(target.ownMethod("value")).toBe(
      "[ownMethod] [baseMethod] base:own:value",
    );
    expect(target.baseMethod("value")).toBe("[baseMethod] base:value");
    expect(target.skippedMethod()).toBe("skipped");
  });

  it("can start wrapping at a specific prototype", () => {
    const logging = createLoggingService();
    const target = new WrappedTarget();

    logging.wrapPrototypeMethods("SparkWallet", target, {
      decorator: decorateResult,
      startAtPrototype: WrappedTargetBase.prototype,
    });

    expect(target.ownMethod("value")).toBe("[baseMethod] base:own:value");
    expect(target.baseMethod("value")).toBe("[baseMethod] base:value");
  });

  it("does not install prototype wrappers when method logging is disabled", () => {
    const logging = createLoggingService();
    const target = new WrappedTarget();
    const originalOwnMethod = target.ownMethod;

    logging.wrapPrototypeMethods("SparkWallet", target);

    expect(target.ownMethod).toBe(originalOwnMethod);
    expect(Object.prototype.hasOwnProperty.call(target, "ownMethod")).toBe(
      false,
    );
  });

  it("installs prototype wrappers when method logging is enabled", () => {
    const logging = createLoggingService();
    const target = new WrappedTarget();

    logging.setMethodLoggingEnabled("SparkWallet", true);
    logging.wrapPrototypeMethods("SparkWallet", target);

    expect(Object.prototype.hasOwnProperty.call(target, "ownMethod")).toBe(
      true,
    );
    expect(target.ownMethod("value")).toBe("base:own:value");
  });

  it("wraps async iterable methods without consuming their output", async () => {
    const logging = createLoggingService();
    const target = new WrappedTarget();

    logging.wrapNamedMethods("SparkWallet", target, ["streamValues"]);

    const values: string[] = [];
    for await (const value of target.streamValues()) {
      values.push(value);
    }

    expect(values).toEqual(["one", "two"]);
  });

  it("ends async iterable method logging when iteration stops early", async () => {
    const messages: string[] = [];
    const logger = {
      trace(message: string) {
        messages.push(message);
      },
    } as unknown as Logger;
    const methodLogger = new MethodCallLogger(logger, { enabled: true });

    const wrapped = methodLogger.wrap(
      "streamValues",
      () => new WrappedTarget().streamValues(),
      undefined,
    ) as () => AsyncIterable<string>;

    for await (const value of wrapped()) {
      expect(value).toBe("one");
      break;
    }

    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatch(/^exit streamValues/);
  });

  it("flushes completed method logs while an async iterable remains active", async () => {
    const messages: string[] = [];
    const logger = {
      trace(message: string) {
        messages.push(message);
      },
    } as unknown as Logger;
    const methodLogger = new MethodCallLogger(logger, { enabled: true });
    async function* longLivedStream() {
      yield "one";
      await new Promise(() => {});
    }
    const wrappedStream = methodLogger.wrap(
      "streamValues",
      () => longLivedStream(),
      undefined,
    ) as () => AsyncIterable<string>;
    const wrappedOtherMethod = methodLogger.wrap(
      "otherMethod",
      () => "ok",
      undefined,
    ) as () => string;

    const iterator = wrappedStream()[Symbol.asyncIterator]();
    await expect(iterator.next()).resolves.toEqual({
      done: false,
      value: "one",
    });

    expect(wrappedOtherMethod()).toBe("ok");
    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatch(/^exit otherMethod/);

    await iterator.return?.();
  });

  it("redacts string args in method entry logs", () => {
    const messages: string[] = [];
    const logger = {
      trace(message: string) {
        messages.push(message);
      },
    } as unknown as Logger;
    const methodLogger = new MethodCallLogger(logger, {
      enabled: true,
      exitOnly: false,
      collapseConsecutive: false,
    });
    const wrapped = methodLogger.wrap(
      "setAuth",
      (sessionToken: unknown) => String(sessionToken).length,
      undefined,
    ) as (sessionToken: string) => number;

    expect(wrapped("secret-session-token")).toBe(20);
    expect(wrapped("secret-session-token")).toBe(20);

    expect(messages[0]).toMatch(
      /^enter setAuth\(String\(20, [0-9a-f]{12}\)\)$/,
    );
    expect(messages[0]).not.toContain("secret-session-token");
    expect(messages[2]).toBe(messages[0]);
  });

  it("wraps promise-like results without requiring catch", async () => {
    const messages: string[] = [];
    const logger = {
      trace(message: string) {
        messages.push(message);
      },
    } as unknown as Logger;
    const methodLogger = new MethodCallLogger(logger, { enabled: true });
    const thenable = {
      then(resolve: (value: string) => void) {
        resolve("ok");
      },
    };

    const wrapped = methodLogger.wrap(
      "thenableMethod",
      () => thenable,
      undefined,
    ) as () => PromiseLike<string>;

    await expect(Promise.resolve(wrapped())).resolves.toBe("ok");
    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatch(/^exit thenableMethod/);
  });

  it("updates instance suffixes without replacing service loggers", () => {
    const logging = createLoggingService({ loggingEnabled: true });
    const logger = logging.logger("SparkWallet");

    logging.setInstanceSuffix("abcd1234");

    expect(logging.logger("SparkWallet")).toBe(logger);
    expect(logger.context).toBe("SparkWallet:abcd1234");
    expect(logger.options.enabled).toBe(true);
  });
});
