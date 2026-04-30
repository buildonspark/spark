import { createWriteStream, mkdirSync } from "node:fs";
import { dirname } from "node:path";

import type { LogFileWriter } from "./log-file-writer.js";

export function createNodeLogFileWriter(filePath: string): LogFileWriter {
  mkdirSync(dirname(filePath), { recursive: true });
  const stream = createWriteStream(filePath, {
    flags: "a",
    encoding: "utf8",
  });
  let failed = false;
  let closed = false;
  let closePromise: Promise<void> | undefined;
  stream.on("error", () => {
    failed = true;
  });

  return {
    write(line: string) {
      if (failed || closed) {
        return;
      }

      try {
        stream.write(`${line}\n`, (error) => {
          if (error) {
            failed = true;
          }
        });
      } catch {
        failed = true;
      }
    },
    close() {
      closed = true;
      closePromise ??= new Promise<void>((resolve) => {
        if (failed || stream.destroyed) {
          resolve();
          return;
        }

        let settled = false;
        const settle = () => {
          if (settled) {
            return;
          }
          settled = true;
          stream.off("error", settle);
          stream.off("finish", settle);
          resolve();
        };

        stream.once("error", settle);
        stream.once("finish", settle);
        try {
          stream.end();
        } catch {
          failed = true;
          settle();
        }
      });
      return closePromise;
    },
  };
}
