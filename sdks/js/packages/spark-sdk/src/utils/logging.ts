import { Logger, LoggingLevel } from "@lightsparkdev/core";

export const LOGGER_NAMES = {
  wasm: "wasm",
} as const;

export type LoggerName = (typeof LOGGER_NAMES)[keyof typeof LOGGER_NAMES];

export class SparkSdkLogger {
  private static loggers = new Map<LoggerName, Logger>();

  static get(name: LoggerName): Logger {
    if (!this.loggers.has(name)) {
      this.loggers.set(name, new Logger(name));
    }

    return this.loggers.get(name)!;
  }

  static setLevel(name: LoggerName, level: LoggingLevel) {
    this.get(name).setLevel(level);
  }

  static setAllLevels(level: LoggingLevel) {
    this.loggers.forEach((logger) => logger.setLevel(level));
  }

  static setEnabled(name: LoggerName, enabled: boolean) {
    this.get(name).setEnabled(enabled);
  }

  static setAllEnabled(enabled: boolean) {
    this.loggers.forEach((logger) => logger.setEnabled(enabled));
  }

  static {
    // Eager-init defaults on module evaluation. This is needed to ensure that the logger is initialized before any other code is executed.
    Object.values(LOGGER_NAMES).forEach((name) => this.get(name));
  }
}
