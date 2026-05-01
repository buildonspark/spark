import { type Logger } from "@lightsparkdev/core";

export const NoopLogger = {
  options: {},
  trace() {},
  debug() {},
  info() {},
  warn() {},
  error() {},
  setOptions() {},
  setLevel() {},
  setEnabled() {},
} as unknown as Logger;

export function formatUrlForLogs(raw: string): string {
  try {
    const url = new URL(raw);
    const base = `${url.protocol}//${url.host}${url.pathname}`;
    return `[path ${base}]`;
  } catch {
    return `[path length=${raw.length}]`;
  }
}
