import http from "http";
import https from "https";
import type { Logger } from "@lightsparkdev/core";
import { Base64 } from "js-base64";
import { throwIfAborted, waitForEvent } from "abort-controller-x";
import {
  Metadata,
  ClientError,
  Status,
  type CallOptions,
} from "nice-grpc-common";
import type { Transport } from "nice-grpc-web/lib/client/Transport.js";
import type { LoggingService } from "../../utils/logging-service.js";

/* This is essentially identical to nice-grpc-web NodeHttpTransport except
   for types and unref on responseStream RPCs to ensure the process can exit
   after the abort signal is triggered */
const UNARY_REQUEST_TIMEOUT_MS = 15_000;
const STREAM_CONNECT_TIMEOUT_MS = 15_000;

export type BareTransportState = {
  nextRequestId: number;
};

type TransportLogLevel = "trace" | "debug";

function makeTransportLevelLogger(
  level: TransportLogLevel,
  path: string,
  requestId: number,
  logger?: Logger,
) {
  if (!logger) {
    return () => {};
  }

  return (message: string) => {
    logger[level]?.(`bareHttpTransport: #${requestId} ${path} ${message}`);
  };
}

function makeTransportLogger(path: string, requestId: number, logger?: Logger) {
  return makeTransportLevelLogger("trace", path, requestId, logger);
}

function makeTransportDebugLogger(
  path: string,
  requestId: number,
  logger?: Logger,
) {
  return makeTransportLevelLogger("debug", path, requestId, logger);
}

export function createBareTransportState(): BareTransportState {
  return {
    nextRequestId: 0,
  };
}

function nextBareTransportRequestId(state: BareTransportState) {
  state.nextRequestId += 1;
  return state.nextRequestId;
}

function createWallClockTimeout(timeoutMs: number, onTimeout: () => void) {
  let timer = setTimeout(onTimeout, timeoutMs);

  return {
    refresh() {
      clearTimeout(timer);
      timer = setTimeout(onTimeout, timeoutMs);
    },
    clear() {
      clearTimeout(timer);
    },
  };
}

export function attachPrematureSocketCloseGuard(
  path: string,
  requestId: number,
  res: http.IncomingMessage,
  logger?: Logger,
) {
  const log = makeTransportLogger(path, requestId, logger);
  const debug = makeTransportDebugLogger(path, requestId, logger);
  const socket = res.socket;
  if (!socket) {
    log("response has no socket to guard");
    return {
      cleanup() {},
    };
  }

  let responseEnded = false;
  let closedPrematurely = false;

  const onResponseEnd = () => {
    responseEnded = true;
  };

  const destroyResponse = (reason: string, error?: Error) => {
    if (responseEnded || closedPrematurely) {
      log(
        `ignoring premature-close guard action after ${reason} (responseEnded=${responseEnded} closedPrematurely=${closedPrematurely})`,
      );
      return;
    }

    // In the failing Bare case the socket can close/error without the response
    // iterator completing. Destroying the response converts that silent stall
    // into the transport error the caller already retries on.
    closedPrematurely = true;
    debug(`forcing response teardown after ${reason}`);
    try {
      res.destroy(
        error ??
          new Error(
            `UNAVAILABLE: response stream closed before completion (${reason}) for ${path}`,
          ),
      );
    } catch {}
  };

  const onSocketClose = () => {
    log("socket close observed by premature-close guard");
    destroyResponse("socket close");
  };

  const onSocketError = (error: Error) => {
    log(`socket error observed by premature-close guard: ${error.message}`);
    destroyResponse(`socket error: ${error.message}`, error);
  };

  const onGuardedResponseEnd = () => {
    log("response end observed by premature-close guard");
    onResponseEnd();
  };

  res.once("end", onGuardedResponseEnd);
  socket.once("close", onSocketClose);
  socket.once("error", onSocketError);

  return {
    cleanup() {
      res.off("end", onGuardedResponseEnd);
      socket.off("close", onSocketClose);
      socket.off("error", onSocketError);
    },
  };
}

export function BareHttpTransport({
  logger,
  logging,
}: {
  logger?: Logger;
  logging?: LoggingService;
} = {}): Transport {
  const transportState = createBareTransportState();
  let transportLogger = logging?.logger("BareHttpTransport") ?? logger;

  const transport = async function* bareHttpTransport({
    url,
    body,
    metadata,
    signal,
    method,
  }) {
    const requestId = nextBareTransportRequestId(transportState);
    const log = makeTransportLogger(method.path, requestId, transportLogger);
    const debug = makeTransportDebugLogger(
      method.path,
      requestId,
      transportLogger,
    );
    let bodyBuffer: Uint8Array | undefined;
    let pipeAbortController: AbortController | undefined;

    log(`starting request url=${url} responseStream=${method.responseStream}`);

    if (!method.requestStream) {
      for await (const chunk of body) {
        bodyBuffer = chunk as Uint8Array;
        break;
      }
      if (bodyBuffer == null) {
        throw new Error("Missing request body for unary request.");
      }
    } else {
      pipeAbortController = new AbortController();
    }

    const { res, removeAbortListener } = await new Promise<{
      res: http.IncomingMessage;
      removeAbortListener: () => void;
    }>((resolve, reject) => {
      let req: http.ClientRequest;
      let clearRequestTimeout = () => {};
      let response: http.IncomingMessage | undefined;
      let requestSetupSettled = false;
      let abortListener = () => {};
      const wallClockTimeout = createWallClockTimeout(
        method.responseStream
          ? STREAM_CONNECT_TIMEOUT_MS
          : UNARY_REQUEST_TIMEOUT_MS,
        () => {
          const error = new Error(
            `UNAVAILABLE: request timed out after ${
              method.responseStream
                ? STREAM_CONNECT_TIMEOUT_MS
                : UNARY_REQUEST_TIMEOUT_MS
            }ms`,
          );
          debug(
            `wall-clock timeout fired${
              response != null
                ? " after response start"
                : " before response start"
            }: ${error.message}`,
          );
          clearRequestTimeout();
          if (response != null) {
            try {
              response.destroy(error);
            } catch {}
          } else {
            failRequestSetup(error);
          }
          try {
            req.destroy(error);
          } catch {}
        },
      );

      const failRequestSetup = (err: Error) => {
        if (requestSetupSettled) {
          return;
        }
        requestSetupSettled = true;
        wallClockTimeout.clear();
        clearRequestTimeout();
        signal.removeEventListener("abort", abortListener);
        try {
          pipeAbortController?.abort();
        } catch {}
        reject(toTransportClientError(method.path, err));
      };

      abortListener = () => {
        log("abort signal received, destroying request");
        wallClockTimeout.clear();
        clearRequestTimeout();
        const abortError = new Error("request aborted");
        if (response != null) {
          try {
            response.destroy(abortError);
          } catch {}
        } else {
          failRequestSetup(abortError);
        }
        try {
          pipeAbortController?.abort();
        } catch {}
        try {
          req.destroy();
        } catch {}
      };

      req = (url.startsWith("https://") ? https : http).request(
        url,
        {
          method: "POST",
          headers: metadataToHeaders(metadata),
        },
        (res) => {
          response = res;
          if (method.responseStream) {
            // Streaming RPCs only use the wall-clock timeout while connecting.
            // Once headers arrive, leave quiet-but-healthy streams alone.
            wallClockTimeout.clear();
          } else {
            wallClockTimeout.refresh();
            // Bare keeps the unary request timeout on the underlying socket
            // until it is explicitly cleared. Once headers arrive, rely on the
            // wall-clock guard for the rest of the response lifecycle instead
            // of leaving a stale socket timeout behind.
            clearRequestTimeout();
          }
          log(
            `response received status=${res.statusCode ?? "unknown"} headers=${JSON.stringify(
              res.headers,
            )}`,
          );
          debug(
            `response received status=${res.statusCode ?? "unknown"} responseStream=${method.responseStream}`,
          );
          // Only unref sockets for response-streaming RPCs so unary calls
          // still keep the process alive while they are in flight.
          if (method.responseStream) {
            try {
              res.socket.unref();
              log("response socket unref applied");
            } catch {}
          }
          res.on("close", () => {
            log("response close event");
          });
          res.on("aborted", () => {
            log("response aborted event");
          });
          res.on("error", (err) => {
            log(`response error event: ${err.message}`);
          });
          res.on("end", () => {
            log("response end event");
          });
          if (requestSetupSettled) {
            // A timeout or abort may already have rejected this request before a
            // late response finally arrived from Bare.
            log(
              "response received after request setup already settled; destroying response",
            );
            try {
              res.destroy();
            } catch {}
            return;
          }
          requestSetupSettled = true;
          resolve({
            res,
            removeAbortListener() {
              wallClockTimeout.clear();
              clearRequestTimeout();
              signal.removeEventListener("abort", abortListener);
            },
          });
        },
      );

      if (!method.responseStream) {
        const onRequestTimeout = () => {
          if (requestSetupSettled) {
            return;
          }

          const error = new Error(
            `UNAVAILABLE: request timed out after ${UNARY_REQUEST_TIMEOUT_MS}ms`,
          );
          debug(`request timeout after ${UNARY_REQUEST_TIMEOUT_MS}ms`);
          clearRequestTimeout();
          // Established stream liveness is handled by the heartbeat listener in
          // SparkWallet, so a unary timeout should only fail the request that
          // actually timed out.
          req.destroy(error);
        };
        clearRequestTimeout = () => {
          try {
            req.off("timeout", onRequestTimeout);
          } catch {}
          try {
            req.setTimeout(0);
          } catch {}
        };
        req.once("timeout", onRequestTimeout);
        req.setTimeout(UNARY_REQUEST_TIMEOUT_MS);
      }

      signal.addEventListener("abort", abortListener);

      req.on("error", (err) => {
        debug(`request error event: ${err.message}`);
        failRequestSetup(err);
      });
      req.on("close", () => {
        log("request close event");
      });
      req.on("finish", () => {
        log("request finish event");
      });
      req.on("timeout", () => {
        log("request timeout event");
      });

      if (bodyBuffer != null) {
        try {
          req.setHeader("Content-Length", bodyBuffer.byteLength);
          req.write(bodyBuffer);
          if (!method.responseStream) {
            wallClockTimeout.refresh();
          }
          log(`wrote unary body bytes=${bodyBuffer.byteLength}`);
          req.end();
        } catch (err) {
          failRequestSetup(err as Error);
        }
      } else {
        pipeBody(pipeAbortController!.signal, body, req).then(
          () => {
            if (!method.responseStream) {
              wallClockTimeout.refresh();
            }
            log("request stream body finished");
            req.end();
          },
          (err) => {
            debug(
              `request stream body failed: ${
                err instanceof Error ? err.message : String(err)
              }`,
            );
            req.destroy(err as Error);
          },
        );
      }
    }).catch((err) => {
      debug(
        `request setup failed: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
      throwIfAborted(signal);
      throw err;
    });

    yield {
      type: "header" as const,
      header: headersToMetadata(res.headers),
    };

    if ((res.statusCode ?? 0) < 200 || (res.statusCode ?? 0) >= 300) {
      debug(`non-2xx response status=${res.statusCode ?? "unknown"}`);
      try {
        const responseText = await new Promise<string>((resolve, reject) => {
          let text = "";
          res.on("data", (chunk) => {
            text += chunk;
          });
          res.on("error", (err) => reject(err));
          res.on("end", () => resolve(text));
        });
        throw new ClientError(
          method.path,
          getStatusFromHttpCode(res.statusCode ?? 0),
          getErrorDetailsFromHttpResponse(res.statusCode ?? 0, responseText),
        );
      } finally {
        try {
          pipeAbortController?.abort();
        } catch {}
        removeAbortListener();
      }
    }

    const prematureCloseGuard = attachPrematureSocketCloseGuard(
      method.path,
      requestId,
      res,
      transportLogger,
    );

    let chunkCount = 0;
    try {
      for await (const data of res) {
        chunkCount++;
        log(`response chunk received bytes=${data.length} chunk=${chunkCount}`);
        yield {
          type: "data" as const,
          data,
        };
      }
      log(`response iterator completed after ${chunkCount} chunks`);
    } catch (err) {
      debug(
        `response iterator error after ${chunkCount} chunks: ${
          err instanceof Error ? err.message : String(err)
        }`,
      );
      throw toTransportClientError(method.path, err);
    } finally {
      log(`response iterator finally after ${chunkCount} chunks`);
      prematureCloseGuard.cleanup();
      try {
        pipeAbortController?.abort();
      } catch {}
      removeAbortListener();
      throwIfAborted(signal);
    }
  };

  return (logging?.wrap(
    "BareHttpTransport",
    "bareHttpTransport",
    transport as (...args: unknown[]) => unknown,
    undefined,
  ) ?? transport) as Transport;
}

function metadataToHeaders(metadata: Metadata): http.OutgoingHttpHeaders {
  const headers: Record<string, string | string[]> = {};
  for (const [key, values] of metadata) {
    headers[key] = values.map((value) =>
      typeof value === "string" ? value : Base64.fromUint8Array(value),
    );
  }
  return headers;
}

function headersToMetadata(headers: http.IncomingHttpHeaders) {
  const metadata = new Metadata();
  for (const [key, headerValue] of Object.entries(headers)) {
    if (headerValue == null) {
      continue;
    }
    const value = Array.isArray(headerValue)
      ? headerValue
      : headerValue.split(/,\s?/);
    if (key.endsWith("-bin")) {
      for (const item of value) {
        metadata.append(key, Base64.toUint8Array(item));
      }
    } else {
      metadata.set(key, value);
    }
  }
  return metadata;
}

function getStatusFromHttpCode(statusCode: number) {
  switch (statusCode) {
    case 400:
      return Status.INTERNAL;
    case 401:
      return Status.UNAUTHENTICATED;
    case 403:
      return Status.PERMISSION_DENIED;
    case 404:
      return Status.UNIMPLEMENTED;
    case 429:
    case 502:
    case 503:
    case 504:
      return Status.UNAVAILABLE;
    default:
      return Status.UNKNOWN;
  }
}

function getErrorDetailsFromHttpResponse(
  statusCode: number,
  responseText: string,
) {
  return (
    `Received HTTP ${statusCode} response: ` +
    (responseText.length > 1000
      ? responseText.slice(0, 1000) + "... (truncated)"
      : responseText)
  );
}

async function pipeBody(
  signal: AbortSignal,
  body: AsyncIterable<Uint8Array>,
  request: http.ClientRequest,
) {
  request.flushHeaders();
  for await (const item of body) {
    throwIfAborted(signal);
    const shouldContinue = request.write(item);
    if (!shouldContinue) {
      await waitForEvent(signal, request as any, "drain");
    }
  }
}

function toTransportClientError(path: string, error: unknown) {
  if (error instanceof ClientError) {
    return error;
  }

  const message = error instanceof Error ? error.message : String(error);
  return new ClientError(path, Status.UNAVAILABLE, message);
}
