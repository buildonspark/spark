import { afterEach, describe, expect, it, jest } from "@jest/globals";
import { EventEmitter } from "events";
import http from "http";
import type { ClientRequest, IncomingMessage } from "http";
import { PassThrough } from "stream";
import { ClientError, Metadata } from "nice-grpc-common";
import type { TransportParams } from "nice-grpc-web/lib/client/Transport.js";
import {
  attachPrematureSocketCloseGuard,
  BareHttpTransport,
} from "../services/connection/bare-http-transport.js";

type MockIncomingMessage = IncomingMessage & {
  destroy: jest.Mock<(error?: Error) => MockIncomingMessage>;
};

type MockClientRequest = ClientRequest & {
  destroy: jest.Mock<(error?: Error) => MockClientRequest>;
  end: jest.Mock<() => MockClientRequest>;
  setHeader: jest.Mock<
    (name: string, value: number | string | string[]) => MockClientRequest
  >;
  setTimeout: jest.Mock<
    (timeout: number, callback?: () => void) => MockClientRequest
  >;
  write: jest.Mock<(chunk: Uint8Array) => boolean>;
};

type TransportMethod = TransportParams["method"];

const queryPendingTransfersMethod = {
  path: "/spark.SparkService/query_pending_transfers",
  requestStream: false,
  responseStream: false,
  requestSerialize: (value: unknown) =>
    value instanceof Uint8Array ? value : new Uint8Array(),
  requestDeserialize: (bytes: Uint8Array): unknown => bytes,
  responseSerialize: (value: unknown) =>
    value instanceof Uint8Array ? value : new Uint8Array(),
  responseDeserialize: (bytes: Uint8Array): unknown => bytes,
  options: {},
} satisfies TransportMethod;

const subscribeToEventsMethod = {
  path: "/spark.SparkService/subscribe_to_events",
  requestStream: false,
  responseStream: true,
  requestSerialize: (value: unknown) =>
    value instanceof Uint8Array ? value : new Uint8Array(),
  requestDeserialize: (bytes: Uint8Array): unknown => bytes,
  responseSerialize: (value: unknown) =>
    value instanceof Uint8Array ? value : new Uint8Array(),
  responseDeserialize: (bytes: Uint8Array): unknown => bytes,
  options: {},
} satisfies TransportMethod;

const queryNodesMethod = {
  path: "/spark.SparkService/query_nodes",
  requestStream: false,
  responseStream: false,
  requestSerialize: (value: unknown) =>
    value instanceof Uint8Array ? value : new Uint8Array(),
  requestDeserialize: (bytes: Uint8Array): unknown => bytes,
  responseSerialize: (value: unknown) =>
    value instanceof Uint8Array ? value : new Uint8Array(),
  responseDeserialize: (bytes: Uint8Array): unknown => bytes,
  options: {},
} satisfies TransportMethod;

function createMockIncomingMessage() {
  const socket = new EventEmitter();
  const res = new EventEmitter() as MockIncomingMessage;

  Object.assign(res, {
    socket,
    destroy: jest.fn<(error?: Error) => MockIncomingMessage>(() => res),
  });

  return { socket, res };
}

function createMockStreamingIncomingMessage() {
  const socket = Object.assign(new EventEmitter(), {
    unref: jest.fn(),
  });
  const res = new PassThrough() as IncomingMessage & PassThrough;

  Object.assign(res, {
    headers: {},
    socket,
    statusCode: 200,
  });

  return { socket, res };
}

function createMockClientRequest() {
  const req = new EventEmitter() as MockClientRequest;

  Object.assign(req, {
    destroy: jest.fn<(error?: Error) => MockClientRequest>(() => req),
    end: jest.fn<() => MockClientRequest>(() => req),
    setHeader: jest.fn<
      (name: string, value: number | string | string[]) => MockClientRequest
    >(() => req),
    setTimeout: jest.fn<
      (timeout: number, callback?: () => void) => MockClientRequest
    >(() => req),
    write: jest.fn<(chunk: Uint8Array) => boolean>(() => true),
  });

  return req;
}

async function* createUnaryBody() {
  await Promise.resolve();
  yield new Uint8Array([1, 2, 3]);
}

async function getRejection(promise: Promise<unknown>) {
  try {
    await promise;
  } catch (error) {
    if (error instanceof Error) {
      return error;
    }
    throw new Error(`Expected Error rejection, received ${String(error)}`);
  }
  throw new Error("Expected promise to reject");
}

afterEach(() => {
  jest.restoreAllMocks();
  jest.useRealTimers();
});

describe("attachPrematureSocketCloseGuard", () => {
  it("destroys the response when the socket closes before the stream ends", () => {
    const { socket, res } = createMockIncomingMessage();
    const guard = attachPrematureSocketCloseGuard(
      "/spark.SparkService/subscribe_to_events",
      1,
      res,
    );

    socket.emit("close");

    expect(res.destroy).toHaveBeenCalledTimes(1);
    const destroyError = res.destroy.mock.calls[0]?.[0];
    expect(destroyError).toBeInstanceOf(Error);
    expect(destroyError?.message).toContain(
      "response stream closed before completion",
    );

    guard.cleanup();
  });

  it("ignores socket close after the response has ended cleanly", () => {
    const { socket, res } = createMockIncomingMessage();
    const guard = attachPrematureSocketCloseGuard(
      "/spark.SparkService/subscribe_to_events",
      1,
      res,
    );

    res.emit("end");
    socket.emit("close");

    expect(res.destroy).not.toHaveBeenCalled();

    guard.cleanup();
  });

  it("does not destroy the response on socket end alone", () => {
    const { socket, res } = createMockIncomingMessage();
    const guard = attachPrematureSocketCloseGuard(
      "/spark.SparkService/subscribe_to_events",
      1,
      res,
    );

    socket.emit("end");

    expect(res.destroy).not.toHaveBeenCalled();

    guard.cleanup();
  });
});

describe("BareHttpTransport", () => {
  it("rejects a unary request when the wall-clock timeout fires before headers arrive", async () => {
    jest.useFakeTimers();

    const req = createMockClientRequest();
    jest.spyOn(http, "request").mockImplementation(() => req);

    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: queryPendingTransfersMethod,
      signal: new AbortController().signal,
      url: "http://example.com/test",
    });

    const nextResultPromise = iterator[Symbol.asyncIterator]().next();
    const errorPromise = getRejection(nextResultPromise);
    await jest.advanceTimersByTimeAsync(15_000);

    const error = await errorPromise;
    expect(error).toBeInstanceOf(Error);
    expect(String(error.message)).toContain("request timed out after 15000ms");
    expect(req.setTimeout).toHaveBeenNthCalledWith(1, 15_000);
    expect(req.setTimeout).toHaveBeenNthCalledWith(2, 0);
    expect(req.destroy).toHaveBeenCalledWith(expect.any(Error));
  });

  it("clears the unary wall-clock timeout when request setup fails synchronously", async () => {
    jest.useFakeTimers();

    const req = createMockClientRequest();
    req.write.mockImplementation(() => {
      throw new Error("AGENT_SUSPENDED: Agent is suspended");
    });
    jest.spyOn(http, "request").mockImplementation(() => req);

    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: queryPendingTransfersMethod,
      signal: new AbortController().signal,
      url: "http://example.com/test",
    });

    const error = await getRejection(iterator[Symbol.asyncIterator]().next());
    expect(error).toBeInstanceOf(ClientError);
    expect(error.message).toContain("AGENT_SUSPENDED: Agent is suspended");
    expect(req.setTimeout).toHaveBeenNthCalledWith(1, 15_000);
    expect(req.setTimeout).toHaveBeenNthCalledWith(2, 0);
    expect(req.destroy).not.toHaveBeenCalled();

    await jest.advanceTimersByTimeAsync(15_000);
    expect(req.destroy).not.toHaveBeenCalled();
  });

  it("clears the unary wall-clock timeout after a non-2xx response", async () => {
    const req = createMockClientRequest();
    let onResponse: ((res: IncomingMessage) => void) | undefined;
    jest
      .spyOn(http, "request")
      .mockImplementation((...args: Parameters<typeof http.request>) => {
        onResponse = args[2];
        return req;
      });

    const { res } = createMockIncomingMessage();
    Object.assign(res, {
      headers: {},
      statusCode: 500,
    });
    const realSetTimeout = global.setTimeout;
    const realClearTimeout = global.clearTimeout;
    const trackedTimeoutHandles: ReturnType<typeof setTimeout>[] = [];
    const clearedTimeoutHandles = new Set<unknown>();

    jest.spyOn(global, "setTimeout").mockImplementation((callback, delay) => {
      const handle = realSetTimeout(callback, delay);
      if (delay === 15_000) {
        trackedTimeoutHandles.push(handle);
      }
      return handle;
    });
    const clearTimeoutMock: typeof clearTimeout = (timeout) => {
      if (timeout != null) {
        clearedTimeoutHandles.add(timeout);
      }
      return realClearTimeout(timeout as ReturnType<typeof setTimeout>);
    };
    jest.spyOn(global, "clearTimeout").mockImplementation(clearTimeoutMock);

    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: queryPendingTransfersMethod,
      signal: new AbortController().signal,
      url: "http://example.com/test",
    })[Symbol.asyncIterator]();

    const headerPromise = iterator.next();
    for (let attempt = 0; attempt < 10 && onResponse == null; attempt++) {
      await new Promise<void>((resolve) => setImmediate(resolve));
    }
    expect(onResponse).toBeDefined();
    onResponse?.(res);
    await headerPromise;

    const errorPromise = getRejection(iterator.next());
    await Promise.resolve();
    res.emit("data", "server exploded");
    res.emit("end");

    const error = await errorPromise;
    expect(error).toBeInstanceOf(ClientError);
    expect(req.setTimeout).toHaveBeenNthCalledWith(1, 15_000);
    expect(req.setTimeout).toHaveBeenNthCalledWith(2, 0);
    expect(trackedTimeoutHandles).not.toHaveLength(0);
    expect(
      trackedTimeoutHandles.every((handle) =>
        clearedTimeoutHandles.has(handle),
      ),
    ).toBe(true);
  });

  it("does not tear down an established stream when a later unary request times out", async () => {
    const streamReq = createMockClientRequest();
    const unaryReq = createMockClientRequest();
    const requests = [streamReq, unaryReq];
    const responseCallbacks: Array<(res: IncomingMessage) => void> = [];

    jest
      .spyOn(http, "request")
      .mockImplementation((...args: Parameters<typeof http.request>) => {
        const onResponse = args[2];
        if (onResponse == null) {
          throw new Error("missing response callback");
        }
        responseCallbacks.push(onResponse);
        const req = requests.shift();
        if (req == null) {
          throw new Error("unexpected extra request");
        }
        return req;
      });
    unaryReq.destroy.mockImplementation((error?: Error) => {
      if (error != null) {
        setImmediate(() => unaryReq.emit("error", error));
      }
      return unaryReq;
    });

    const { res: streamRes } = createMockStreamingIncomingMessage();
    const streamDestroySpy = jest.spyOn(streamRes, "destroy");
    const transport = BareHttpTransport();

    const streamIterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: subscribeToEventsMethod,
      signal: new AbortController().signal,
      url: "http://example.com/stream",
    })[Symbol.asyncIterator]();

    const streamHeaderPromise = streamIterator.next();
    for (
      let attempt = 0;
      attempt < 10 && responseCallbacks.length === 0;
      attempt++
    ) {
      await new Promise<void>((resolve) => setImmediate(resolve));
    }
    expect(responseCallbacks).toHaveLength(1);
    responseCallbacks[0]!(streamRes);
    await streamHeaderPromise;

    const streamDataPromise = streamIterator.next();

    const unaryIterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: queryNodesMethod,
      signal: new AbortController().signal,
      url: "http://example.com/query",
    })[Symbol.asyncIterator]();

    const unaryErrorPromise = getRejection(unaryIterator.next());
    for (
      let attempt = 0;
      attempt < 10 && unaryReq.setTimeout.mock.calls.length === 0;
      attempt++
    ) {
      await new Promise<void>((resolve) => setImmediate(resolve));
    }
    expect(unaryReq.setTimeout).toHaveBeenCalledWith(15_000);

    unaryReq.emit("timeout");

    const unaryError = await unaryErrorPromise;

    expect(streamDestroySpy).not.toHaveBeenCalled();
    expect(unaryError).toBeInstanceOf(ClientError);
    expect(String(unaryError.message)).toContain(
      "request timed out after 15000ms",
    );

    streamRes.destroy(new Error("test cleanup"));
    await streamDataPromise.then(
      () => null,
      () => null,
    );
  });

  it("unrefs both request and response sockets for response-streaming RPCs", async () => {
    const req = createMockClientRequest();
    const requestSocket = { unref: jest.fn() };
    Object.assign(req, {
      socket: requestSocket,
    });
    let onResponse: ((res: IncomingMessage) => void) | undefined;
    jest
      .spyOn(http, "request")
      .mockImplementation((...args: Parameters<typeof http.request>) => {
        onResponse = args[2];
        return req;
      });

    const { res, socket: responseSocket } =
      createMockStreamingIncomingMessage();
    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: subscribeToEventsMethod,
      signal: new AbortController().signal,
      url: "http://example.com/stream",
    })[Symbol.asyncIterator]();

    const headerPromise = iterator.next();
    for (let attempt = 0; attempt < 10 && onResponse == null; attempt++) {
      await new Promise<void>((resolve) => setImmediate(resolve));
    }
    expect(onResponse).toBeDefined();
    onResponse?.(res);
    await headerPromise;

    expect(requestSocket.unref).toHaveBeenCalledTimes(1);
    expect(responseSocket.unref).toHaveBeenCalledTimes(1);

    const streamDataPromise = iterator.next();
    res.destroy(new Error("test cleanup"));
    await streamDataPromise.then(
      () => null,
      () => null,
    );
  });

  it("unrefs response-streaming request sockets assigned after request creation", async () => {
    const req = createMockClientRequest();
    let onResponse: ((res: IncomingMessage) => void) | undefined;
    jest
      .spyOn(http, "request")
      .mockImplementation((...args: Parameters<typeof http.request>) => {
        onResponse = args[2];
        return req;
      });

    const { res, socket: responseSocket } =
      createMockStreamingIncomingMessage();
    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: subscribeToEventsMethod,
      signal: new AbortController().signal,
      url: "http://example.com/stream",
    })[Symbol.asyncIterator]();

    const headerPromise = iterator.next();
    for (let attempt = 0; attempt < 10 && onResponse == null; attempt++) {
      await new Promise<void>((resolve) => setImmediate(resolve));
    }
    expect(onResponse).toBeDefined();

    const requestSocket = { unref: jest.fn() };
    req.emit("socket", requestSocket);
    onResponse?.(res);
    await headerPromise;

    expect(requestSocket.unref).toHaveBeenCalledTimes(1);
    expect(responseSocket.unref).toHaveBeenCalledTimes(1);

    const streamDataPromise = iterator.next();
    res.destroy(new Error("test cleanup"));
    await streamDataPromise.then(
      () => null,
      () => null,
    );
  });
});
