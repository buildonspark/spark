import {
  type CallOptions,
  ClientError,
  type ClientMiddlewareCall,
  Status,
} from "nice-grpc-common";

const DEADLINE_EXCEEDED_MESSAGE = "Deadline exceeded";

type DeadlineCallOptions = CallOptions & { deadline?: Date | number };

function getRemainingDeadlineMs(deadline: Date | number): number | undefined {
  const deadlineMs = deadline instanceof Date ? deadline.getTime() : deadline;
  if (!Number.isFinite(deadlineMs)) {
    return undefined;
  }
  return deadlineMs - Date.now();
}

/**
 * Enforces the `deadline` call option by translating it into an abort signal.
 * nice-grpc and nice-grpc-web honor `signal` but ignore `deadline`, so without
 * this a deadline-only call has no client-side timeout and a never-delivered
 * response hangs forever. The native React Native transport handles deadlines
 * itself; this is only wired into the nice-grpc and nice-grpc-web managers.
 */
export async function* deadlineMiddleware<Req, Res>(
  call: ClientMiddlewareCall<Req, Res>,
  options: DeadlineCallOptions,
) {
  if (options.deadline === undefined) {
    return yield* call.next(call.request as Req, options);
  }

  const remainingMs = getRemainingDeadlineMs(options.deadline);
  if (remainingMs === undefined) {
    return yield* call.next(call.request as Req, options);
  }
  // Already past: reject without dispatching, since the transport may not honor
  // an already-aborted signal.
  if (remainingMs <= 0) {
    throw new ClientError(
      call.method.path,
      Status.DEADLINE_EXCEEDED,
      DEADLINE_EXCEEDED_MESSAGE,
    );
  }

  const controller = new AbortController();
  const externalSignal = options.signal;
  const onExternalAbort = () => controller.abort(externalSignal?.reason);

  if (externalSignal?.aborted) {
    controller.abort(externalSignal.reason);
  } else {
    externalSignal?.addEventListener("abort", onExternalAbort, { once: true });
  }

  let deadlineExceeded = false;
  const onDeadline = () => {
    deadlineExceeded = true;
    controller.abort();
  };

  const timer = setTimeout(onDeadline, remainingMs);

  try {
    return yield* call.next(call.request as Req, {
      ...options,
      signal: controller.signal,
    });
  } catch (error) {
    if (deadlineExceeded) {
      throw new ClientError(
        call.method.path,
        Status.DEADLINE_EXCEEDED,
        DEADLINE_EXCEEDED_MESSAGE,
      );
    }
    throw error;
  } finally {
    clearTimeout(timer);
    externalSignal?.removeEventListener("abort", onExternalAbort);
  }
}
