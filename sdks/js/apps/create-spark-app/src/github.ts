import { get } from "node:https";
import { createGunzip } from "node:zlib";
import { type IncomingMessage } from "node:http";
import { extract as tarExtract } from "tar";

const REPO = "buildonspark/spark";
const DEFAULT_BRANCH = "main";

/** Directories to exclude (matched with trailing /) */
const EXCLUDED_DIRS = [
  ".turbo",
  "node_modules",
  "dist",
  "build",
  ".next",
  ".gradle",
];

/** Exact filenames to exclude */
const EXCLUDED_FILES = [
  "CHANGELOG.md",
  ".DS_Store",
  ".spark-mnemonic",
  ".issuer-mnemonic",
];

export async function downloadAndExtractTemplate(
  templateDir: string,
  targetDir: string,
  branch: string = DEFAULT_BRANCH,
): Promise<void> {
  const tarballUrl = `https://codeload.github.com/${REPO}/tar.gz/${branch}`;
  // GitHub replaces / with - in the tarball root directory name
  const safeBranch = branch.replaceAll("/", "-");
  const prefix = `spark-${safeBranch}/sdks/js/examples/${templateDir}/`;
  const stripCount = prefix.split("/").filter(Boolean).length;

  return new Promise<void>((resolve, reject) => {
    get(tarballUrl, (res) => {
      if (res.statusCode === 301 || res.statusCode === 302) {
        const location = res.headers.location;
        if (!location) {
          reject(new Error("Redirect without location header"));
          return;
        }
        get(location, (redirectRes) => {
          if (redirectRes.statusCode !== 200) {
            reject(
              new Error(
                `Failed to download template: HTTP ${redirectRes.statusCode}`,
              ),
            );
            return;
          }
          extractStream(redirectRes, prefix, stripCount, targetDir)
            .then(resolve)
            .catch(reject);
        }).on("error", reject);
        return;
      }

      if (res.statusCode !== 200) {
        reject(
          new Error(`Failed to download template: HTTP ${res.statusCode}`),
        );
        return;
      }

      extractStream(res, prefix, stripCount, targetDir)
        .then(resolve)
        .catch(reject);
    }).on("error", reject);
  });
}

async function extractStream(
  stream: IncomingMessage,
  prefix: string,
  stripCount: number,
  targetDir: string,
): Promise<void> {
  let filesExtracted = 0;

  return new Promise<void>((resolve, reject) => {
    stream
      .pipe(createGunzip())
      .pipe(
        tarExtract({
          cwd: targetDir,
          strip: stripCount,
          filter: (path: string) => {
            if (!path.startsWith(prefix)) return false;
            const relative = path.slice(prefix.length);
            const segments = relative.split("/");
            const isExcludedDir = segments.some((s) =>
              EXCLUDED_DIRS.includes(s),
            );
            const fileName = segments[segments.length - 1] ?? "";
            const isExcludedFile = EXCLUDED_FILES.includes(fileName);
            const included = !isExcludedDir && !isExcludedFile;
            if (included) filesExtracted++;
            return included;
          },
        }),
      )
      .on("finish", () => {
        if (filesExtracted === 0) {
          reject(
            new Error(
              `Template not found in repository. No files matched "${prefix}"`,
            ),
          );
        } else {
          resolve();
        }
      })
      .on("error", reject);
  });
}
