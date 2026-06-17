import {
  type CallOptions,
  ClientError,
  type ClientMiddlewareCall,
  Status,
} from "nice-grpc-common";
import { deadlineMiddleware } from "../services/connection/deadline-middleware.js";

// The timeout contract (a stalled call must reject with DEADLINE_EXCEEDED
// rather than hang) is only observable against a stalled transport, which has
// no representation at the public SDK boundary, so it is exercised through the
// `call.next` transport seam like the existing connection middleware tests.

const METHOD_PATH = "/test.Service/Method";

type NextFn = (
  request: unknown,
  options: CallOptions,
) => AsyncGenerator<never, unknown, undefined>;

function makeCall(next: NextFn): ClientMiddlewareCall<unknown, unknown> {
  return {
    method: {
      path: METHOD_PATH,
      requestStream: false,
      responseStream: false,
      options: {},
    },
    requestStream: false,
    responseStream: false,
    request: {},
    next,
  } as unknown as ClientMiddlewareCall<unknown, unknown>;
}

async function drain(
  generator: AsyncGenerator<unknown, unknown, undefined>,
): Promise<unknown> {
  let result = await generator.next();
  while (!result.done) {
    result = await generator.next();
  }
  return result.value;
}

// Resolves with `response` unless the call signal aborts first.
function respondUnlessAborted(response: unknown): NextFn {
  // eslint-disable-next-line require-yield
  return async function* (_request, options) {
    await new Promise<void>((resolve, reject) => {
      const signal = options.signal;
      if (signal?.aborted) {
        reject(
          signal.reason instanceof Error ? signal.reason : new Error("aborted"),
        );
        return;
      }
      const timer = setTimeout(resolve, 10);
      signal?.addEventListener(
        "abort",
        () => {
          clearTimeout(timer);
          reject(
            signal.reason instanceof Error
              ? signal.reason
              : new Error("aborted"),
          );
        },
        { once: true },
      );
    });
    return response;
  };
}

// Models a stalled response: never resolves on its own, only on abort.
function hangUntilAborted(): NextFn {
  // eslint-disable-next-line require-yield
  return async function* (_request, options) {
    await new Promise<void>((_resolve, reject) => {
      const signal = options.signal;
      if (signal?.aborted) {
        reject(
          signal.reason instanceof Error ? signal.reason : new Error("aborted"),
        );
        return;
      }
      signal?.addEventListener(
        "abort",
        () =>
          reject(
            signal.reason instanceof Error
              ? signal.reason
              : new Error("aborted"),
          ),
        { once: true },
      );
    });
    return undefined;
  };
}

describe("deadlineMiddleware", () => {
  it("passes the response through unchanged when no deadline is set", async () => {
    const response = { ok: true };
    const result = await drain(
      deadlineMiddleware(makeCall(respondUnlessAborted(response)), {}),
    );
    expect(result).toBe(response);
  });

  it("returns the response when the deadline is not exceeded", async () => {
    const response = { ok: true };
    const result = await drain(
      deadlineMiddleware(makeCall(respondUnlessAborted(response)), {
        deadline: Date.now() + 10_000,
      }),
    );
    expect(result).toBe(response);
  });

  it("rejects with DEADLINE_EXCEEDED when a stalled call passes its deadline", async () => {
    await expect(
      drain(
        deadlineMiddleware(makeCall(hangUntilAborted()), {
          deadline: Date.now() + 20,
        }),
      ),
    ).rejects.toMatchObject({
      code: Status.DEADLINE_EXCEEDED,
      path: METHOD_PATH,
    });
  });

  it("rejects immediately when the deadline is already in the past", async () => {
    await expect(
      drain(
        deadlineMiddleware(makeCall(hangUntilAborted()), {
          deadline: Date.now() - 1,
        }),
      ),
    ).rejects.toMatchObject({ code: Status.DEADLINE_EXCEEDED });
  });

  it("propagates a caller-initiated abort without masking it as DEADLINE_EXCEEDED", async () => {
    const controller = new AbortController();
    const cancelled = new ClientError(
      METHOD_PATH,
      Status.CANCELLED,
      "caller aborted",
    );
    const promise = drain(
      deadlineMiddleware(makeCall(hangUntilAborted()), {
        deadline: Date.now() + 10_000,
        signal: controller.signal,
      }),
    );
    controller.abort(cancelled);
    await expect(promise).rejects.toBe(cancelled);
  });
});
