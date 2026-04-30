import {
  Logger,
  LoggingLevel,
  type LoggerOptions,
  type LoggerOptionsArg,
  type LoggingLevelArg,
} from "@lightsparkdev/core";
import type { LogFileWriter } from "./log-file-writer.js";

type LoggerOutputOptions = {
  console: boolean;
  fileWriter?: LogFileWriter;
};

type ConsoleMethod = "log" | "warn" | "error";

const LOGGING_LEVEL_FROM_NAME = {
  TRACE: LoggingLevel.Trace,
  DEBUG: LoggingLevel.Debug,
  INFO: LoggingLevel.Info,
  WARN: LoggingLevel.Warn,
  ERROR: LoggingLevel.Error,
} as const;

export function createSdkLogger(
  loggerContext: string,
  loggerOptions: LoggerOptionsArg,
  output: LoggerOutputOptions,
): Logger {
  if (output.console && !output.fileWriter) {
    return new Logger(loggerContext, loggerOptions);
  }

  return new MultiOutputLogger(
    loggerContext,
    loggerOptions,
    output,
  ) as unknown as Logger;
}

function normalizeLoggingLevel(level: LoggingLevelArg): LoggingLevel {
  if (typeof level === "number") {
    return level;
  }

  const normalizedLevel = level.toUpperCase();
  const lowerLevel = normalizedLevel.toLowerCase();
  const titleLevel = `${lowerLevel[0]?.toUpperCase() ?? ""}${lowerLevel.slice(
    1,
  )}`;

  if (
    level !== normalizedLevel &&
    level !== lowerLevel &&
    level !== titleLevel
  ) {
    throw new Error(
      `Invalid LoggingLevelName casing (expected UPPER, lower, or Title): ${level}`,
    );
  }

  if (normalizedLevel in LOGGING_LEVEL_FROM_NAME) {
    return LOGGING_LEVEL_FROM_NAME[
      normalizedLevel as keyof typeof LOGGING_LEVEL_FROM_NAME
    ];
  }

  throw new Error(`Invalid LoggingLevelName: ${level}`);
}

function formatLogValue(value: unknown): string {
  if (value instanceof Error) {
    return value.stack ?? value.message;
  }

  if (typeof value === "string") {
    return value;
  }

  try {
    return JSON.stringify(value) ?? String(value);
  } catch {
    return String(value);
  }
}

class MultiOutputLogger {
  context: string;
  options: LoggerOptions = {
    enabled: false,
    timestamps: true,
    level: LoggingLevel.Info,
  };

  private readonly consoleEnabled: boolean;
  private readonly fileWriter?: LogFileWriter;

  constructor(
    loggerContext: string,
    loggerOptions: LoggerOptionsArg,
    output: LoggerOutputOptions,
  ) {
    this.context = loggerContext;
    this.consoleEnabled = output.console;
    this.fileWriter = output.fileWriter;
    this.setOptions(loggerOptions);
  }

  setLevel(level: LoggingLevel) {
    this.options.level = level;
  }

  setEnabled(enabled: boolean, level: LoggingLevel = LoggingLevel.Info) {
    this.options.enabled = enabled;
    this.options.level = level;
  }

  setOptions(options: LoggerOptionsArg) {
    if (options.enabled !== undefined) {
      this.options.enabled = options.enabled;
    }
    if (options.timestamps !== undefined) {
      this.options.timestamps = options.timestamps;
    }
    if (options.level !== undefined) {
      this.options.level = normalizeLoggingLevel(options.level);
    }
  }

  trace(message: string, ...rest: unknown[]) {
    this.write(LoggingLevel.Trace, "log", message, rest);
  }

  debug(message: string, ...rest: unknown[]) {
    this.write(LoggingLevel.Debug, "log", message, rest);
  }

  info(message: string, ...rest: unknown[]) {
    this.write(LoggingLevel.Info, "log", message, rest);
  }

  warn(message: string, ...rest: unknown[]) {
    this.write(LoggingLevel.Warn, "warn", message, rest);
  }

  error(message: string, ...rest: unknown[]) {
    this.write(LoggingLevel.Error, "error", message, rest);
  }

  private write(
    level: LoggingLevel,
    consoleMethod: ConsoleMethod,
    message: string,
    rest: unknown[],
  ) {
    if (!this.options.enabled || this.options.level > level) {
      return;
    }

    const formattedMessage = this.formatMessage(message);
    if (this.consoleEnabled) {
      console[consoleMethod](formattedMessage, ...rest);
    }

    this.fileWriter?.write(this.formatFileLine(formattedMessage, rest));
  }

  private formatMessage(message: string) {
    return `${this.getTimestamp()}[${this.context}] ${message}`;
  }

  private formatFileLine(message: string, rest: unknown[]) {
    if (rest.length === 0) {
      return message;
    }

    return `${message} ${rest.map(formatLogValue).join(" ")}`;
  }

  private getTimestamp() {
    if (this.options.timestamps) {
      return `${new Date().toISOString()} `;
    }
    return "";
  }
}
