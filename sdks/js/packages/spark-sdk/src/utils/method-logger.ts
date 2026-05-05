import { type Logger } from "@lightsparkdev/core";
import { sha256 } from "@noble/hashes/sha2";
import { bytesToHex, concatBytes, utf8ToBytes } from "@noble/hashes/utils";
import type {
  MethodLoggingConfig,
  MethodLoggingOptions,
} from "../services/wallet-config.js";

const DEFAULT_METHOD_LOGGING_CONFIG: Required<MethodLoggingConfig> = {
  enabled: false,
  collapseConsecutive: true,
  excludedMethods: [],
  exitOnly: true,
};
const STRING_FINGERPRINT_LENGTH = 12;
const STRING_FINGERPRINT_SALT = createStringFingerprintSalt();

type NormalizedMethodLoggingConfig = {
  enabled: boolean;
  collapseConsecutive: boolean;
  excludedMethods: Set<string>;
  exitOnly: boolean;
};

type MethodLogCluster = {
  methodName: string;
  count: number;
  totalDurationMs: number;
};

type MethodLoggingContext = {
  methodName: string;
  startTime: number;
  longLived?: boolean;
};

function createStringFingerprintSalt() {
  const salt = new Uint8Array(16);
  if (typeof globalThis.crypto?.getRandomValues === "function") {
    globalThis.crypto.getRandomValues(salt);
    return salt;
  }

  return sha256(
    utf8ToBytes(`${Date.now()}:${Math.random()}:${Math.random()}`),
  ).slice(0, salt.length);
}

function fingerprintString(value: string) {
  return bytesToHex(
    sha256(concatBytes(STRING_FINGERPRINT_SALT, utf8ToBytes(value))),
  ).slice(0, STRING_FINGERPRINT_LENGTH);
}

export class MethodCallLogger {
  private logger: Logger;
  private config: NormalizedMethodLoggingConfig;
  private enabled: boolean;
  private methodLoggingDepth = 0;
  private activeLongLivedMethodDepth = 0;
  private pendingMethodLog: MethodLogCluster | null = null;

  constructor(logger: Logger, methodLogging: MethodLoggingOptions | undefined) {
    this.logger = logger;
    this.config = this.resolveMethodLoggingConfig(methodLogging);
    this.enabled = this.config.enabled;
  }

  public setEnabled(enabled: boolean) {
    if (this.enabled === enabled) {
      return;
    }

    this.enabled = enabled;
    this.config.enabled = enabled;
    if (!enabled) {
      this.pendingMethodLog = null;
      this.methodLoggingDepth = 0;
      this.activeLongLivedMethodDepth = 0;
    }
  }

  public isEnabled() {
    return this.enabled;
  }

  public setLogger(logger: Logger) {
    this.logger = logger;
  }

  public wrap(
    methodName: string,
    method: (...args: unknown[]) => unknown,
    receiver: unknown,
  ): (...args: unknown[]) => unknown {
    return (...args: unknown[]) => {
      const context = this.beginMethodLogging(methodName, args);

      try {
        const result = Reflect.apply(method, receiver, args);
        if (MethodCallLogger.isPromiseLike(result)) {
          return Promise.resolve(result).then(
            (value) => {
              this.endMethodLogging(context, "success");
              return value;
            },
            (error) => {
              this.endMethodLogging(context, "error", error);
              throw error;
            },
          );
        }

        if (MethodCallLogger.isAsyncIterable(result)) {
          return this.wrapAsyncIterable(context, result);
        }

        this.endMethodLogging(context, "success");
        return result;
      } catch (error) {
        this.endMethodLogging(context, "error", error);
        throw error;
      }
    };
  }

  public flushPendingLogs() {
    this.flushCoalescedLogs();
    this.methodLoggingDepth = 0;
    this.activeLongLivedMethodDepth = 0;
  }

  private beginMethodLogging(
    methodName: string,
    args: unknown[],
  ): MethodLoggingContext | null {
    if (!this.enabled) {
      return null;
    }

    if (this.isMethodLoggingExcluded(methodName)) {
      this.flushCoalescedLogs();
      return null;
    }

    if (this.config.collapseConsecutive) {
      this.flushCoalescedLogsIfNeeded(methodName);
    } else {
      this.flushCoalescedLogs();
    }

    const shouldLogEntry =
      !this.config.collapseConsecutive ||
      !this.pendingMethodLog ||
      this.pendingMethodLog.methodName !== methodName;

    if (shouldLogEntry && !this.config.exitOnly) {
      this.logMethodEntry(methodName, args);
    }

    this.methodLoggingDepth += 1;

    return {
      methodName,
      startTime: Date.now(),
    };
  }

  private endMethodLogging(
    context: MethodLoggingContext | null,
    status: "success" | "error",
    error?: unknown,
  ) {
    if (!context || !this.enabled) {
      return;
    }

    const durationMs = Date.now() - context.startTime;
    this.recordMethodCompletion(context.methodName, durationMs, status, error);

    this.methodLoggingDepth = Math.max(0, this.methodLoggingDepth - 1);
    if (context.longLived) {
      this.activeLongLivedMethodDepth = Math.max(
        0,
        this.activeLongLivedMethodDepth - 1,
      );
    }

    if (
      this.methodLoggingDepth === 0 ||
      (this.activeLongLivedMethodDepth > 0 &&
        this.methodLoggingDepth === this.activeLongLivedMethodDepth)
    ) {
      this.flushCoalescedLogs();
    }
  }

  private recordMethodCompletion(
    methodName: string,
    durationMs: number,
    status: "success" | "error",
    error?: unknown,
  ) {
    if (!this.config.collapseConsecutive) {
      if (status === "error") {
        this.logMethodError(methodName, error, durationMs);
      } else {
        this.logMethodExit(methodName, durationMs);
      }
      return;
    }

    if (status === "error") {
      this.flushCoalescedLogs();
      this.logMethodError(methodName, error, durationMs);
      return;
    }

    if (
      this.pendingMethodLog &&
      this.pendingMethodLog.methodName === methodName
    ) {
      this.pendingMethodLog.count += 1;
      this.pendingMethodLog.totalDurationMs += durationMs;
      return;
    }

    this.flushCoalescedLogs();
    this.pendingMethodLog = {
      methodName,
      count: 1,
      totalDurationMs: durationMs,
    };
  }

  private flushCoalescedLogsIfNeeded(nextMethodName?: string) {
    if (!this.pendingMethodLog || !this.config.collapseConsecutive) {
      return;
    }

    if (
      typeof nextMethodName === "string" &&
      this.pendingMethodLog.methodName === nextMethodName
    ) {
      return;
    }

    this.flushCoalescedLogs();
  }

  private flushCoalescedLogs() {
    if (!this.pendingMethodLog) {
      return;
    }

    this.logMethodExit(
      this.pendingMethodLog.methodName,
      this.pendingMethodLog.totalDurationMs,
      this.pendingMethodLog.count,
    );
    this.pendingMethodLog = null;
  }

  private resolveMethodLoggingConfig(
    methodLogging: MethodLoggingOptions | undefined,
  ): NormalizedMethodLoggingConfig {
    const base = DEFAULT_METHOD_LOGGING_CONFIG;

    if (typeof methodLogging === "boolean" || methodLogging === undefined) {
      return {
        enabled: methodLogging ?? base.enabled,
        collapseConsecutive: base.collapseConsecutive,
        excludedMethods: new Set(base.excludedMethods),
        exitOnly: base.exitOnly,
      };
    }

    return {
      enabled: methodLogging.enabled ?? base.enabled,
      collapseConsecutive:
        methodLogging.collapseConsecutive ?? base.collapseConsecutive,
      excludedMethods: new Set(
        methodLogging.excludedMethods ?? base.excludedMethods,
      ),
      exitOnly: methodLogging.exitOnly ?? base.exitOnly,
    };
  }

  private isMethodLoggingExcluded(methodName: string) {
    return this.config.excludedMethods.has(methodName);
  }

  private logMethodEntry(methodName: string, args: unknown[]) {
    const argsSummary = this.formatMethodArgs(args);
    const suffix = argsSummary.length > 0 ? `(${argsSummary})` : "()";
    this.logger.trace(`enter ${methodName}${suffix}`);
  }

  private logMethodExit(methodName: string, durationMs: number, count = 1) {
    const countSuffix = count > 1 ? ` x${count}` : "";
    const durationSuffix =
      count > 1 ? ` (+${durationMs}ms total)` : ` (+${durationMs}ms)`;
    this.logger.trace(`exit ${methodName}${countSuffix}${durationSuffix}`);
  }

  private logMethodError(
    methodName: string,
    error: unknown,
    durationMs: number,
  ) {
    const message = error instanceof Error ? error.message : String(error);
    this.logger.trace(`error ${methodName} (+${durationMs}ms): ${message}`);
  }

  private formatMethodArgs(args: unknown[]) {
    return args
      .map((arg) => this.formatMethodArg(arg))
      .filter((value) => value.length > 0)
      .join(", ");
  }

  private formatMethodArg(value: unknown): string {
    if (value === undefined) {
      return "undefined";
    }

    if (value === null) {
      return "null";
    }

    if (typeof value === "string") {
      return `String(${value.length}, ${fingerprintString(value)})`;
    }

    if (
      typeof value === "number" ||
      typeof value === "boolean" ||
      typeof value === "bigint"
    ) {
      return String(value);
    }

    if (value instanceof Uint8Array) {
      return `Uint8Array(len=${value.length})`;
    }

    if (Array.isArray(value)) {
      return `Array(len=${value.length})`;
    }

    if (typeof value === "function") {
      return "[Function]";
    }

    if (typeof value === "object") {
      const constructorName = (value as { constructor?: { name?: string } })
        .constructor?.name;
      if (constructorName && constructorName !== "Object") {
        return `[${constructorName}]`;
      }

      const keys = Object.keys(value);
      return `Object(keys=${keys.slice(0, 5).join(",")}${
        keys.length > 5 ? ",..." : ""
      })`;
    }

    return String(value);
  }

  private static isPromiseLike(value: unknown): value is PromiseLike<unknown> {
    return (
      typeof value === "object" &&
      value !== null &&
      "then" in value &&
      typeof (value as PromiseLike<unknown>).then === "function"
    );
  }

  private wrapAsyncIterable(
    context: MethodLoggingContext | null,
    result: AsyncIterable<unknown>,
  ): AsyncIterable<unknown> {
    this.markLongLivedMethodLogging(context);
    const endMethodLogging = this.endMethodLogging.bind(this);

    return (async function* () {
      let status: "success" | "error" = "success";
      let caughtError: unknown;

      try {
        return yield* result;
      } catch (error) {
        status = "error";
        caughtError = error;
        throw error;
      } finally {
        endMethodLogging(context, status, caughtError);
      }
    })();
  }

  private markLongLivedMethodLogging(context: MethodLoggingContext | null) {
    if (!context || context.longLived) {
      return;
    }

    context.longLived = true;
    this.activeLongLivedMethodDepth += 1;
  }

  private static isAsyncIterable(
    value: unknown,
  ): value is AsyncIterable<unknown> {
    return (
      typeof value === "object" &&
      value !== null &&
      Symbol.asyncIterator in value &&
      typeof (value as AsyncIterable<unknown>)[Symbol.asyncIterator] ===
        "function"
    );
  }
}
