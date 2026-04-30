export type LogFileWriter = {
  write(line: string): void;
  close?: () => void | Promise<void>;
};

export type LogFileWriterFactory = (filePath: string) => LogFileWriter;

let logFileWriterFactory: LogFileWriterFactory | undefined;

export function setLogFileWriterFactory(
  factory: LogFileWriterFactory | undefined,
) {
  logFileWriterFactory = factory;
}

export function createLogFileWriter(
  filePath: string | undefined,
): LogFileWriter | undefined {
  if (!filePath) {
    return undefined;
  }

  const factory = logFileWriterFactory;
  if (!factory) {
    throw new Error("log.file is only supported by the Node.js SDK entrypoint");
  }

  let writer: LogFileWriter | undefined;
  let closed = false;
  let failed = false;

  return {
    write(line: string) {
      if (closed || failed) {
        return;
      }

      try {
        writer ??= factory(filePath);
        writer.write(line);
      } catch {
        failed = true;
      }
    },
    close() {
      closed = true;
      try {
        return writer?.close?.();
      } catch {
        failed = true;
      }
    },
  };
}
