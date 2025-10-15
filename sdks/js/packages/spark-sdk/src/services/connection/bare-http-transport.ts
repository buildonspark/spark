import http from "http";
import https from "https";
import { Base64 } from "js-base64";
import { throwIfAborted, waitForEvent } from "abort-controller-x";
import {
  Metadata,
  ClientError,
  Status,
  type CallOptions,
} from "nice-grpc-common";
import type { Transport } from "nice-grpc-web/lib/client/Transport.js";

/* This is essentially identical to nice-grpc-web NodeHttpTransport except
   for types and unref on responseStream RPCs to ensure the process can exit
   after the abort signal is triggered */
export function BareHttpTransport(): Transport {
  return async function* bareHttpTransport({
    url,
    body,
    metadata,
    signal,
    method,
  }) {
    let bodyBuffer: Uint8Array | undefined;
    let pipeAbortController: AbortController | undefined;

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

      const abortListener = () => {
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
          // Only unref sockets for response-streaming RPCs so unary calls
          // still keep the process alive while they are in flight.
          if (method.responseStream) {
            try {
              res.socket.unref();
            } catch {}
          }
          resolve({
            res,
            removeAbortListener() {
              signal.removeEventListener("abort", abortListener);
            },
          });
        },
      );

      signal.addEventListener("abort", abortListener);

      req.on("error", (err) => {
        reject(err);
      });

      if (bodyBuffer != null) {
        try {
          req.setHeader("Content-Length", bodyBuffer.byteLength);
          req.write(bodyBuffer);
          req.end();
        } catch (err) {
          reject(err);
        }
      } else {
        pipeBody(pipeAbortController!.signal, body, req).then(
          () => {
            req.end();
          },
          (err) => {
            req.destroy(err as Error);
          },
        );
      }
    }).catch((err) => {
      throwIfAborted(signal);
      throw err;
    });

    yield {
      type: "header" as const,
      header: headersToMetadata(res.headers),
    };

    if ((res.statusCode ?? 0) < 200 || (res.statusCode ?? 0) >= 300) {
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
    }

    try {
      for await (const data of res) {
        yield {
          type: "data" as const,
          data,
        };
      }
    } finally {
      try {
        pipeAbortController?.abort();
      } catch {}
      removeAbortListener();
      throwIfAborted(signal);
    }
  };
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

// tmp
