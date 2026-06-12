import { Base64 } from "js-base64";
import type { Channel } from "nice-grpc";
import type { RetryOptions } from "nice-grpc-client-middleware-retry";
import type {
  CallOptions,
  ClientMiddleware,
  ClientMiddlewareCall,
  MethodDescriptor,
} from "nice-grpc-common";
import { ClientError, Metadata, Status } from "nice-grpc-common";
import { NativeModules } from "react-native";
import { uuidv7 } from "uuidv7";
import { getClientEnv } from "../../constants.js";
import { SparkRequestError } from "../../errors/types.js";
import type { SparkServiceDefinition } from "../../proto/spark.js";
import type { SparkAuthnServiceDefinition } from "../../proto/spark_authn.js";
import type { SparkTokenServiceDefinition } from "../../proto/spark_token.js";
import type { SparkCallOptions } from "../../types/grpc.js";
import type { LoggingService } from "../../utils/logging-service.js";
import type { WalletConfigService } from "../config.js";
import { getMonotonicTime } from "../time-sync.js";
import { ConnectionManager, type AuthMode } from "./connection.js";

type NativeMetadata = Record<string, string[]>;

type NativeUnaryCallParams = {
  requestId: string;
  address: string;
  isStreamClientType: boolean;
  path: string;
  metadata: NativeMetadata;
  requestBase64: string;
};

type NativeUnaryCallResponse = {
  statusCode: number;
  statusMessage?: string;
  responseBase64?: string;
  headers?: NativeMetadata;
  trailers?: NativeMetadata;
};

type NativeServerStreamStartParams = Omit<NativeUnaryCallParams, "requestId">;

type NativeServerStreamStartResponse = {
  streamId: string;
};

type NativeServerStreamEvent =
  | {
      kind: "headers";
      metadata?: NativeMetadata;
    }
  | {
      kind: "message";
      messageBase64: string;
    }
  | {
      kind: "complete";
      statusCode: number;
      statusMessage?: string;
      trailers?: NativeMetadata;
    };

type NativeServerStreamControlParams = {
  streamId: string;
};

type NativeCloseChannelParams = {
  address: string;
  isStreamClientType: boolean;
};

type NativeUnaryCallCancelParams = {
  requestId: string;
};

type SparkGrpcNativeModule = {
  grpcUnaryCall(
    params: NativeUnaryCallParams,
  ): Promise<NativeUnaryCallResponse>;
  grpcUnaryCallCancel(params: NativeUnaryCallCancelParams): Promise<void>;
  grpcServerStreamStart(
    params: NativeServerStreamStartParams,
  ): Promise<NativeServerStreamStartResponse>;
  grpcServerStreamNext(
    params: NativeServerStreamControlParams,
  ): Promise<NativeServerStreamEvent>;
  grpcServerStreamCancel(
    params: NativeServerStreamControlParams,
  ): Promise<void>;
  grpcCloseChannel(params: NativeCloseChannelParams): Promise<void>;
};

type MessageFns<T> = {
  encode(message: T): { finish(): Uint8Array };
  decode(input: Uint8Array): T;
  fromPartial(object: unknown): T;
};

type ServiceMethodDef<Req, Res> = {
  name: string;
  requestType: MessageFns<Req>;
  requestStream: boolean;
  responseType: MessageFns<Res>;
  responseStream: boolean;
  options: MethodDescriptor["options"];
};

type SupportedServiceDefinition = {
  fullName: string;
  methods: Record<string, ServiceMethodDef<unknown, unknown>>;
};

type ReactNativeGrpcChannel = {
  address: string;
  isStreamClientType: boolean;
  close: () => Promise<void>;
};

type DeadlineCallOptions = CallOptions & {
  deadline?: Date | number;
};

type NormalizedRetryOptions = {
  retry: boolean;
  retryMaxAttempts: number;
  retryBaseDelayMs: number;
  retryMaxDelayMs: number;
  retryableStatuses: Status[];
  onRetryableError: (
    error: ClientError,
    attempt: number,
    delayMs: number,
  ) => void;
};

type NativeClientMiddleware = ClientMiddleware<RetryOptions, object>;

const DEFAULT_RETRY_OPTIONS: NormalizedRetryOptions = {
  retry: true,
  retryMaxAttempts: 3,
  retryBaseDelayMs: 1000,
  retryMaxDelayMs: 10000,
  retryableStatuses: [Status.UNAVAILABLE, Status.CANCELLED],
  onRetryableError: () => {},
};
const VALID_METADATA_KEY_RE = /^[0-9a-z_.-]+$/;

export function hasNativeGrpcModule(): boolean {
  return Boolean(NativeModules.SparkGrpcModule);
}

function getNativeGrpcModule(): SparkGrpcNativeModule {
  const nativeModule = NativeModules.SparkGrpcModule as
    | SparkGrpcNativeModule
    | undefined;
  if (!nativeModule) {
    throw new SparkRequestError("Native Spark gRPC module is unavailable", {
      moduleName: "SparkGrpcModule",
    });
  }
  return nativeModule;
}

function toGrpcStatus(code: number | undefined): Status {
  if (typeof code !== "number" || Status[code] === undefined) {
    return Status.UNKNOWN;
  }
  return code;
}

function normalizeRetryStatus(
  value: Status | keyof typeof Status,
): Status | undefined {
  if (typeof value === "number") {
    return toGrpcStatus(value);
  }
  const raw = Status[value];
  if (typeof raw === "number") {
    return raw;
  }
  return undefined;
}

function toNativeMetadata(metadata: Metadata): NativeMetadata {
  const nativeMetadata: NativeMetadata = {};
  for (const [key, values] of metadata) {
    const normalizedKey = key.toLowerCase();
    if (!VALID_METADATA_KEY_RE.test(normalizedKey)) {
      continue;
    }

    nativeMetadata[normalizedKey] = values.map((value) =>
      typeof value === "string" ? value : Base64.fromUint8Array(value),
    );
  }
  return nativeMetadata;
}

function fromNativeMetadata(nativeMetadata?: NativeMetadata): Metadata {
  const metadata = new Metadata();
  if (!nativeMetadata) {
    return metadata;
  }

  for (const [key, values] of Object.entries(nativeMetadata)) {
    const normalizedKey = key.toLowerCase();
    if (!VALID_METADATA_KEY_RE.test(normalizedKey)) {
      continue;
    }

    if (!Array.isArray(values) || values.length === 0) {
      continue;
    }
    if (normalizedKey.endsWith("-bin")) {
      metadata.set(
        normalizedKey,
        values.map((value) => Base64.toUint8Array(value)),
      );
    } else {
      metadata.set(normalizedKey, values);
    }
  }

  return metadata;
}

function isAsyncIterable(value: unknown): value is AsyncIterable<unknown> {
  return (
    value != null &&
    typeof value === "object" &&
    Symbol.asyncIterator in value &&
    typeof (value as AsyncIterable<unknown>)[Symbol.asyncIterator] ===
      "function"
  );
}

function createCancelledError(path: string): ClientError {
  return new ClientError(path, Status.CANCELLED, "Request aborted");
}

function createDeadlineExceededError(path: string): ClientError {
  return new ClientError(path, Status.DEADLINE_EXCEEDED, "Deadline exceeded");
}

function createAbortError(path: string, signal?: AbortSignal): ClientError {
  return signal?.reason instanceof ClientError
    ? signal.reason
    : createCancelledError(path);
}

function throwIfAborted(path: string, signal?: AbortSignal) {
  if (signal?.aborted) {
    throw createAbortError(path, signal);
  }
}

function getDeadlineTimeoutMs(options: CallOptions): number | undefined {
  const { deadline } = options as DeadlineCallOptions;
  if (deadline === undefined) {
    return undefined;
  }

  const deadlineMs = deadline instanceof Date ? deadline.getTime() : deadline;
  if (!Number.isFinite(deadlineMs)) {
    return undefined;
  }

  return Math.max(0, deadlineMs - Date.now());
}

function createCallControl(path: string, options: CallOptions) {
  const timeoutMs = getDeadlineTimeoutMs(options);
  if (timeoutMs === undefined) {
    return {
      signal: options.signal,
      cleanup: () => {},
      throwIfAborted: () => throwIfAborted(path, options.signal),
    };
  }

  const controller = new AbortController();
  let deadlineExceeded = false;

  const abortFromSignal = () => {
    if (!controller.signal.aborted) {
      controller.abort(options.signal?.reason);
    }
  };

  if (options.signal?.aborted) {
    abortFromSignal();
  } else {
    options.signal?.addEventListener("abort", abortFromSignal, { once: true });
  }

  const onDeadline = () => {
    deadlineExceeded = true;
    controller.abort(createDeadlineExceededError(path));
  };
  let timeout: ReturnType<typeof setTimeout> | undefined;
  if (timeoutMs <= 0) {
    onDeadline();
  } else {
    timeout = setTimeout(onDeadline, timeoutMs);
  }

  return {
    signal: controller.signal,
    cleanup: () => {
      if (timeout !== undefined) {
        clearTimeout(timeout);
      }
      options.signal?.removeEventListener("abort", abortFromSignal);
    },
    throwIfAborted: () => {
      if (deadlineExceeded) {
        throw createDeadlineExceededError(path);
      }
      throwIfAborted(path, options.signal);
      throwIfAborted(path, controller.signal);
    },
  };
}

async function delayWithSignal(
  path: string,
  delayMs: number,
  signal?: AbortSignal,
) {
  throwIfAborted(path, signal);

  if (delayMs <= 0) {
    return;
  }

  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, delayMs);

    const onAbort = () => {
      clearTimeout(timer);
      signal?.removeEventListener("abort", onAbort);
      reject(createAbortError(path, signal));
    };

    signal?.addEventListener("abort", onAbort);
  });
}

async function resolveUnaryGenerator<Response>(
  generator: AsyncGenerator<Response, Response | void, undefined>,
): Promise<Response> {
  let result = await generator.next();
  while (!result.done) {
    result = await generator.next();
  }
  if (result.value === undefined) {
    throw new Error("Unary gRPC call returned no response");
  }
  return result.value;
}

function getRetryOptions(
  withRetries: boolean,
  options?: SparkCallOptions,
): NormalizedRetryOptions {
  if (!withRetries || options?.retry === false) {
    return {
      ...DEFAULT_RETRY_OPTIONS,
      retry: false,
      retryMaxAttempts: 1,
    };
  }

  return {
    ...DEFAULT_RETRY_OPTIONS,
    retry: options?.retry ?? DEFAULT_RETRY_OPTIONS.retry,
    retryMaxAttempts:
      options?.retryMaxAttempts ?? DEFAULT_RETRY_OPTIONS.retryMaxAttempts,
    retryBaseDelayMs:
      options?.retryBaseDelayMs ?? DEFAULT_RETRY_OPTIONS.retryBaseDelayMs,
    retryMaxDelayMs:
      options?.retryMaxDelayMs ?? DEFAULT_RETRY_OPTIONS.retryMaxDelayMs,
    retryableStatuses:
      options?.retryableStatuses
        ?.map(normalizeRetryStatus)
        .filter((status): status is Status => status !== undefined) ??
      DEFAULT_RETRY_OPTIONS.retryableStatuses,
    onRetryableError:
      options?.onRetryableError ?? DEFAULT_RETRY_OPTIONS.onRetryableError,
  };
}

function extractErrorMessage(error: unknown): string {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

function asClientError(path: string, error: unknown): ClientError {
  if (error instanceof ClientError) {
    return error;
  }
  return new ClientError(path, Status.UNKNOWN, extractErrorMessage(error));
}

async function safeCancelServerStream(streamId: string) {
  try {
    await getNativeGrpcModule().grpcServerStreamCancel({ streamId });
  } catch {
    // no-op
  }
}

export class ConnectionManagerReactNative extends ConnectionManager {
  constructor(
    config: WalletConfigService,
    authMode: AuthMode = "identity",
    logging?: LoggingService,
  ) {
    super(config, authMode, logging);
  }

  protected getMonotonicTime(): number {
    return getMonotonicTime();
  }

  protected prepareMetadata(metadata: Metadata): Metadata {
    return super.prepareMetadata(metadata).set("X-Client-Env", getClientEnv());
  }

  protected createChannelWithTLS(
    address: string,
    isStreamClientType: boolean = false,
  ): Promise<Channel> {
    return Promise.resolve({
      address,
      isStreamClientType,
      close: () =>
        getNativeGrpcModule().grpcCloseChannel({
          address,
          isStreamClientType,
        }),
    } as unknown as Channel);
  }

  protected createGrpcClient<T>(
    definition:
      | SparkAuthnServiceDefinition
      | SparkServiceDefinition
      | SparkTokenServiceDefinition,
    channel: Channel,
    withRetries: boolean,
    middleware?: NativeClientMiddleware,
    channelKey?: string,
  ): Promise<T & { close?: () => void | Promise<void> }> {
    const nativeChannel = channel as unknown as ReactNativeGrpcChannel;
    const serviceDef = definition as unknown as SupportedServiceDefinition;
    const methodEntries = Object.entries(serviceDef.methods);
    const client: Record<string, unknown> = {};

    for (const [methodName, methodDef] of methodEntries) {
      if (methodDef.requestStream) {
        throw new Error(
          `React Native gRPC client does not support request streaming (${methodName})`,
        );
      }

      const methodPath = `/${serviceDef.fullName}/${methodDef.name}`;
      const descriptor: MethodDescriptor = {
        path: methodPath,
        requestStream: methodDef.requestStream,
        responseStream: methodDef.responseStream,
        options: methodDef.options ?? {},
      };

      if (methodDef.responseStream) {
        client[methodName] = (
          request: unknown,
          options?: SparkCallOptions,
        ): AsyncIterable<unknown> =>
          this.executeServerStreamCall({
            descriptor,
            methodDef,
            request,
            options,
            middleware,
            channel: nativeChannel,
          });
      } else {
        client[methodName] = async (
          request: unknown,
          options?: SparkCallOptions,
        ): Promise<unknown> =>
          this.executeUnaryCallWithRetry({
            descriptor,
            methodDef,
            request,
            options,
            middleware,
            withRetries,
            channel: nativeChannel,
          });
      }
    }

    return Promise.resolve({
      ...(client as T),
      close: channelKey
        ? () => ConnectionManager.releaseChannelAsync(channelKey)
        : undefined,
    });
  }

  private async executeUnaryCallWithRetry({
    descriptor,
    methodDef,
    request,
    options,
    middleware,
    withRetries,
    channel,
  }: {
    descriptor: MethodDescriptor;
    methodDef: ServiceMethodDef<unknown, unknown>;
    request: unknown;
    options?: SparkCallOptions;
    middleware?: NativeClientMiddleware;
    withRetries: boolean;
    channel: ReactNativeGrpcChannel;
  }): Promise<unknown> {
    const retryOptions = getRetryOptions(withRetries, options);
    const maxAttempts = Math.max(1, retryOptions.retryMaxAttempts);
    const retryControl = createCallControl(descriptor.path, options ?? {});

    try {
      for (let attempt = 1; attempt <= maxAttempts; attempt++) {
        retryControl.throwIfAborted();
        try {
          return await this.executeUnaryCall({
            descriptor,
            methodDef,
            request,
            options,
            middleware,
            channel,
          });
        } catch (error) {
          const clientError = asClientError(descriptor.path, error);
          retryControl.throwIfAborted();

          const canRetry =
            retryOptions.retry &&
            attempt < maxAttempts &&
            retryOptions.retryableStatuses.includes(clientError.code);
          if (!canRetry) {
            throw clientError;
          }

          const retryDelay = Math.min(
            retryOptions.retryMaxDelayMs,
            retryOptions.retryBaseDelayMs * 2 ** (attempt - 1),
          );
          retryOptions.onRetryableError(clientError, attempt, retryDelay);
          await delayWithSignal(
            descriptor.path,
            retryDelay,
            retryControl.signal,
          );
          retryControl.throwIfAborted();
        }
      }
    } finally {
      retryControl.cleanup();
    }

    throw new ClientError(
      descriptor.path,
      Status.UNKNOWN,
      "Unary gRPC retry loop exhausted unexpectedly",
    );
  }

  private async executeUnaryCall({
    descriptor,
    methodDef,
    request,
    options,
    middleware,
    channel,
  }: {
    descriptor: MethodDescriptor;
    methodDef: ServiceMethodDef<unknown, unknown>;
    request: unknown;
    options?: SparkCallOptions;
    middleware?: NativeClientMiddleware;
    channel: ReactNativeGrpcChannel;
  }): Promise<unknown> {
    const runCall = (
      nextRequest: unknown,
      callOptions: CallOptions,
    ): AsyncGenerator<never, unknown, undefined> =>
      // nice-grpc unary middleware expects an async generator whose return value is the response.
      // eslint-disable-next-line require-yield
      async function* (
        this: ConnectionManagerReactNative,
      ): AsyncGenerator<never, unknown, undefined> {
        if (isAsyncIterable(nextRequest)) {
          throw new Error("Request streaming is not supported");
        }
        const response = await this.callNativeUnary({
          descriptor,
          methodDef,
          request: nextRequest,
          options: callOptions,
          channel,
        });
        return response;
      }.call(this);

    if (!middleware) {
      return resolveUnaryGenerator(runCall(request, options ?? {}));
    }

    const call: ClientMiddlewareCall<unknown, unknown> = {
      method: descriptor,
      requestStream: false,
      request,
      responseStream: false,
      next: runCall,
    };

    const middlewareOptions = (options ?? {}) as CallOptions &
      Partial<RetryOptions>;
    return resolveUnaryGenerator(middleware(call, middlewareOptions));
  }

  private executeServerStreamCall({
    descriptor,
    methodDef,
    request,
    options,
    middleware,
    channel,
  }: {
    descriptor: MethodDescriptor;
    methodDef: ServiceMethodDef<unknown, unknown>;
    request: unknown;
    options?: SparkCallOptions;
    middleware?: NativeClientMiddleware;
    channel: ReactNativeGrpcChannel;
  }): AsyncIterable<unknown> {
    const runCall = (
      nextRequest: unknown,
      callOptions: CallOptions,
    ): AsyncGenerator<unknown, void, undefined> =>
      async function* (
        this: ConnectionManagerReactNative,
      ): AsyncGenerator<unknown, void, undefined> {
        if (isAsyncIterable(nextRequest)) {
          throw new Error("Request streaming is not supported");
        }
        yield* this.callNativeServerStream({
          descriptor,
          methodDef,
          request: nextRequest,
          options: callOptions,
          channel,
        });
      }.call(this);

    if (!middleware) {
      return runCall(request, options ?? {});
    }

    const call: ClientMiddlewareCall<unknown, unknown> = {
      method: descriptor,
      requestStream: false,
      request,
      responseStream: true,
      next: runCall,
    };

    const middlewareOptions = (options ?? {}) as CallOptions &
      Partial<RetryOptions>;
    return middleware(call, middlewareOptions);
  }

  private async callNativeUnary({
    descriptor,
    methodDef,
    request,
    options,
    channel,
  }: {
    descriptor: MethodDescriptor;
    methodDef: ServiceMethodDef<unknown, unknown>;
    request: unknown;
    options: CallOptions;
    channel: ReactNativeGrpcChannel;
  }): Promise<unknown> {
    const callControl = createCallControl(descriptor.path, options);
    callControl.throwIfAborted();

    const requestMessage = methodDef.requestType.fromPartial(request ?? {});
    const requestBytes = methodDef.requestType.encode(requestMessage).finish();
    const requestMetadata = this.prepareMetadata(Metadata(options.metadata));

    const requestId = uuidv7();
    const onAbort = () => {
      void getNativeGrpcModule().grpcUnaryCallCancel({ requestId });
    };

    let response: NativeUnaryCallResponse;
    callControl.signal?.addEventListener("abort", onAbort);
    try {
      response = await getNativeGrpcModule().grpcUnaryCall({
        requestId,
        address: channel.address,
        isStreamClientType: channel.isStreamClientType,
        path: descriptor.path,
        requestBase64: Base64.fromUint8Array(requestBytes),
        metadata: toNativeMetadata(requestMetadata),
      });
    } catch (error) {
      callControl.throwIfAborted();
      throw asClientError(descriptor.path, error);
    } finally {
      callControl.signal?.removeEventListener("abort", onAbort);
      callControl.cleanup();
    }

    callControl.throwIfAborted();

    const headers = fromNativeMetadata(response.headers);
    options.onHeader?.(headers);

    const trailers = fromNativeMetadata(response.trailers);
    options.onTrailer?.(trailers);

    const status = toGrpcStatus(response.statusCode);
    if (status !== Status.OK) {
      throw new ClientError(
        descriptor.path,
        status,
        response.statusMessage ?? "gRPC unary call failed",
      );
    }

    const responseBytes = response.responseBase64
      ? Base64.toUint8Array(response.responseBase64)
      : new Uint8Array(0);
    return methodDef.responseType.decode(responseBytes);
  }

  private async *callNativeServerStream({
    descriptor,
    methodDef,
    request,
    options,
    channel,
  }: {
    descriptor: MethodDescriptor;
    methodDef: ServiceMethodDef<unknown, unknown>;
    request: unknown;
    options: CallOptions;
    channel: ReactNativeGrpcChannel;
  }): AsyncGenerator<unknown, void, undefined> {
    const callControl = createCallControl(descriptor.path, options);
    callControl.throwIfAborted();

    const requestMessage = methodDef.requestType.fromPartial(request ?? {});
    const requestBytes = methodDef.requestType.encode(requestMessage).finish();
    const requestMetadata = this.prepareMetadata(Metadata(options.metadata));

    let streamId: string | undefined;
    let receivedTerminalEvent = false;
    const onAbort = () => {
      if (streamId) {
        void safeCancelServerStream(streamId);
      }
    };

    callControl.signal?.addEventListener("abort", onAbort);
    try {
      const startResp = await getNativeGrpcModule().grpcServerStreamStart({
        address: channel.address,
        isStreamClientType: channel.isStreamClientType,
        path: descriptor.path,
        requestBase64: Base64.fromUint8Array(requestBytes),
        metadata: toNativeMetadata(requestMetadata),
      });
      streamId = startResp.streamId;
      callControl.throwIfAborted();

      while (true) {
        callControl.throwIfAborted();
        const event = await getNativeGrpcModule().grpcServerStreamNext({
          streamId,
        });
        callControl.throwIfAborted();

        if (event.kind === "headers") {
          options.onHeader?.(fromNativeMetadata(event.metadata));
          continue;
        }

        if (event.kind === "message") {
          const payload = Base64.toUint8Array(event.messageBase64);
          yield methodDef.responseType.decode(payload);
          continue;
        }

        if (event.kind === "complete") {
          receivedTerminalEvent = true;
          options.onTrailer?.(fromNativeMetadata(event.trailers));
          const status = toGrpcStatus(event.statusCode);
          if (status !== Status.OK) {
            throw new ClientError(
              descriptor.path,
              status,
              event.statusMessage ?? "gRPC stream call failed",
            );
          }
          return;
        }

        throw new ClientError(
          descriptor.path,
          Status.UNKNOWN,
          "Unexpected native stream event",
        );
      }
    } catch (error) {
      callControl.throwIfAborted();
      throw asClientError(descriptor.path, error);
    } finally {
      callControl.signal?.removeEventListener("abort", onAbort);
      callControl.cleanup();
      if (streamId && !receivedTerminalEvent) {
        await safeCancelServerStream(streamId);
      }
    }
  }
}
