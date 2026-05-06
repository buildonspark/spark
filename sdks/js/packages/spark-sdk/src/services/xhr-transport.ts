import type { Logger } from "@lightsparkdev/core";
import { throwIfAborted } from "abort-controller-x";
import { Base64 } from "js-base64";
import { ClientError, Metadata, Status } from "nice-grpc-common";
import type {
  Frame,
  Transport,
  TransportParams,
} from "nice-grpc-web/lib/client/Transport.js";
import { NoopLogger } from "../utils/logging.js";
import type { LoggingService } from "../utils/logging-service.js";

class GrpcCallData {
  responseHeaders: Metadata = new Metadata();
  responseChunks: Uint8Array[] = [];
  grpcStatus: Status = Status.UNKNOWN;
  statusMessage: string = "";
}

export interface XHRTransportConfig {
  credentials?: boolean;
  logger?: Logger;
  logging?: LoggingService;
}

async function xhrPost(
  url: string,
  metadata: Metadata,
  requestBody: BodyInit,
  config?: XHRTransportConfig,
  logger: Logger = NoopLogger,
): Promise<GrpcCallData> {
  const callData: GrpcCallData = new GrpcCallData();
  return new Promise(function (resolve, reject) {
    // TODO - Support fallback for node?
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url, true);
    xhr.withCredentials = config?.credentials ?? true;
    xhr.responseType = "arraybuffer";

    for (const [key, values] of metadata) {
      for (const value of values) {
        xhr.setRequestHeader(
          key,
          typeof value === "string" ? value : Base64.fromUint8Array(value),
        );
      }
    }

    xhr.onreadystatechange = function () {
      if (xhr.readyState === XMLHttpRequest.HEADERS_RECEIVED) {
        callData.responseHeaders = headersToMetadata(
          xhr.getAllResponseHeaders(),
          logger,
        );
      } else if (xhr.readyState === XMLHttpRequest.DONE) {
        resolve(callData);
      }
    };
    xhr.onerror = function () {
      callData.statusMessage = getErrorDetailsFromHttpResponse(
        xhr.status,
        xhr.statusText,
      );
    };
    xhr.onloadend = function () {
      callData.responseChunks.push(new Uint8Array(xhr.response as ArrayBuffer));
      callData.grpcStatus = getStatusFromHttpCode(xhr.status);
    };

    xhr.send(requestBody);
  });
}

function concatenateChunks(chunks: Uint8Array[]): Uint8Array {
  // Using the performant method vs spread syntax: https://stackoverflow.com/a/60590943
  let totalSize = 0;
  for (const chunk of chunks) {
    totalSize += chunk.length;
  }
  const newData = new Uint8Array(totalSize);
  let setIndex = 0;
  for (const chunk of chunks) {
    newData.set(chunk, setIndex);
    setIndex += chunk.length;
  }
  return newData;
}

/**
 * Transport for browsers based on `XMLHttpRequest` API.
 */
export function XHRTransport(config?: XHRTransportConfig): Transport {
  const logger =
    config?.logging?.logger("XHRTransport") ?? config?.logger ?? NoopLogger;

  const transport = async function* xhrTransport({
    url,
    body,
    metadata,
    signal,
    method,
  }: TransportParams): AsyncGenerator<Frame, void, undefined> {
    let requestBody: BodyInit;

    if (!method.requestStream) {
      let bodyBuffer: Uint8Array | undefined;

      for await (const chunk of body) {
        bodyBuffer = chunk;

        break;
      }

      requestBody = bodyBuffer!.slice();
    } else {
      let iterator: AsyncIterator<Uint8Array, void, undefined> | undefined;

      requestBody = new ReadableStream<Uint8Array>({
        type: "bytes",
        start() {
          iterator = body[Symbol.asyncIterator]();
        },

        async pull(controller) {
          if (!iterator) {
            throw new Error("Request body iterator was not initialized");
          }
          const { done, value } = await iterator.next();

          if (done) {
            controller.close();
          } else {
            controller.enqueue(value.slice());
          }
        },
        async cancel() {
          await iterator?.return?.();
        },
      });
    }

    const xhrData = await xhrPost(url, metadata, requestBody, config, logger);

    yield {
      type: "header",
      header: xhrData.responseHeaders,
    };

    if (xhrData.grpcStatus !== Status.OK) {
      const decoder = new TextDecoder();
      const message = decoder.decode(concatenateChunks(xhrData.responseChunks));
      logger.warn(`${message} ${xhrData.statusMessage}`);
      throw new ClientError(
        method.path,
        xhrData.grpcStatus,
        `status=${xhrData.statusMessage}, message=${message}`,
      );
    }

    throwIfAborted(signal);

    try {
      for (const xhrChunk of xhrData.responseChunks) {
        if (xhrChunk != null) {
          yield {
            type: "data",
            data: xhrChunk,
          };
        }
      }
    } finally {
      throwIfAborted(signal);
    }
  };

  return (config?.logging?.wrap(
    "XHRTransport",
    "xhrTransport",
    transport as (...args: unknown[]) => unknown,
    undefined,
  ) ?? transport) as Transport;
}

function headersToMetadata(
  headers: string,
  logger: Logger = NoopLogger,
): Metadata {
  const metadata = new Metadata();
  const arr = headers.trim().split(/[\r\n]+/);

  arr.forEach((line) => {
    const parts = line.split(": ");
    const header = parts.shift() ?? "";
    const value = parts.join(": ");

    if (header.endsWith("-bin")) {
      try {
        metadata.set(header, Base64.toUint8Array(value));
      } catch (e) {
        logger.warn(
          `Failed to decode binary metadata ${header}: ${
            e instanceof Error ? e.message : String(e)
          }`,
        );
        metadata.set(header, value);
      }
    } else {
      metadata.set(header, value);
    }
  });
  return metadata;
}

function getStatusFromHttpCode(statusCode: number): Status {
  switch (statusCode) {
    case 200:
      return Status.OK;
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
): string {
  return (
    `Received HTTP ${statusCode} response: ` +
    (responseText?.length > 1000
      ? responseText?.slice(0, 1000) + "... (truncated)"
      : responseText)
  );
}
