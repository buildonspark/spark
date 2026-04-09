import { afterEach, describe, expect, it, jest } from "@jest/globals";
import { EventEmitter } from "events";
import http from "http";
import type { ClientRequest, IncomingMessage } from "http";
import { ClientError, Metadata } from "nice-grpc-common";
import {
  attachPrematureSocketCloseGuard,
  BareHttpTransport,
  createBareTransportState,
  registerActiveResponseStream,
  resetActiveResponseStreamsForOrigin,
} from "../services/connection/bare-http-transport.js";

type MockIncomingMessage = IncomingMessage & {
  destroy: ReturnType<typeof jest.fn>;
};

type MockClientRequest = ClientRequest & {
  destroy: ReturnType<typeof jest.fn>;
  end: ReturnType<typeof jest.fn>;
  setHeader: ReturnType<typeof jest.fn>;
  setTimeout: ReturnType<typeof jest.fn>;
  write: ReturnType<typeof jest.fn>;
};

function createMockIncomingMessage() {
  const socket = new EventEmitter();
  const res = new EventEmitter() as MockIncomingMessage;

  Object.assign(res, {
    socket,
    destroy: jest.fn(),
  });

  return { socket, res };
}

function createMockClientRequest() {
  const req = new EventEmitter() as MockClientRequest;

  Object.assign(req, {
    destroy: jest.fn(),
    end: jest.fn(),
    setHeader: jest.fn(),
    setTimeout: jest.fn(),
    write: jest.fn(),
  });

  return req;
}

async function* createUnaryBody() {
  yield new Uint8Array([1, 2, 3]);
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
    expect(res.destroy.mock.calls[0]?.[0]).toBeInstanceOf(Error);
    expect(String(res.destroy.mock.calls[0]?.[0]?.message)).toContain(
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
  it("does not destroy a newer active stream when an older unary request times out", () => {
    const transportState = createBareTransportState();
    const destroy = jest.fn();
    const log = jest.fn();
    const unregister = registerActiveResponseStream(
      transportState,
      "https://example.com",
      47,
      destroy,
      log,
    );

    resetActiveResponseStreamsForOrigin(
      transportState,
      "https://example.com",
      44,
      new Error("UNAVAILABLE: request timed out after 15000ms"),
    );

    expect(destroy).not.toHaveBeenCalled();
    expect(log).toHaveBeenCalledWith(
      expect.stringContaining(
        "skipping active response stream reset because request #44 timed out before this newer stream was created",
      ),
    );

    unregister();
  });

  it("destroys an older active stream when a newer unary request times out", () => {
    const transportState = createBareTransportState();
    const destroy = jest.fn();
    const log = jest.fn();
    const unregister = registerActiveResponseStream(
      transportState,
      "https://example.com",
      15,
      destroy,
      log,
    );

    const error = new Error("UNAVAILABLE: request timed out after 15000ms");
    resetActiveResponseStreamsForOrigin(
      transportState,
      "https://example.com",
      37,
      error,
    );

    expect(destroy).toHaveBeenCalledWith(error);
    expect(log).toHaveBeenCalledWith(
      expect.stringContaining(
        "destroying active response stream because request #37 timed out",
      ),
    );

    unregister();
  });

  it("isolates active stream tracking between transport instances", () => {
    const firstTransportState = createBareTransportState();
    const secondTransportState = createBareTransportState();
    const firstDestroy = jest.fn();
    const secondDestroy = jest.fn();
    const firstUnregister = registerActiveResponseStream(
      firstTransportState,
      "https://example.com",
      15,
      firstDestroy,
      jest.fn(),
    );
    const secondUnregister = registerActiveResponseStream(
      secondTransportState,
      "https://example.com",
      15,
      secondDestroy,
      jest.fn(),
    );

    resetActiveResponseStreamsForOrigin(
      firstTransportState,
      "https://example.com",
      37,
      new Error("UNAVAILABLE: request timed out after 15000ms"),
    );

    expect(firstDestroy).toHaveBeenCalledWith(expect.any(Error));
    expect(secondDestroy).not.toHaveBeenCalled();

    firstUnregister();
    secondUnregister();
  });

  it("rejects a unary request when the wall-clock timeout fires before headers arrive", async () => {
    jest.useFakeTimers();

    const req = createMockClientRequest();
    jest
      .spyOn(http, "request")
      .mockImplementation((() => req) as unknown as typeof http.request);

    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: {
        path: "/spark.SparkService/query_pending_transfers",
        requestStream: false,
        responseStream: false,
      } as any,
      signal: new AbortController().signal,
      url: "http://example.com/test",
    });

    const nextResultPromise = iterator[Symbol.asyncIterator]().next();
    const errorPromise = nextResultPromise.then(
      () => null,
      (error) => error,
    );
    await jest.advanceTimersByTimeAsync(15_000);

    const error = await errorPromise;
    expect(error).toBeInstanceOf(Error);
    expect(String((error as Error).message)).toContain(
      "request timed out after 15000ms",
    );
    expect(req.destroy).toHaveBeenCalledWith(expect.any(Error));
  });

  it("clears the unary wall-clock timeout after a non-2xx response", async () => {
    const req = createMockClientRequest();
    let onResponse: ((res: IncomingMessage) => void) | undefined;
    jest.spyOn(http, "request").mockImplementation(((
      ...args: Parameters<typeof http.request>
    ) => {
      onResponse = args[2];
      return req;
    }) as typeof http.request);

    const { res } = createMockIncomingMessage();
    Object.assign(res, {
      headers: {},
      statusCode: 500,
    });
    const realSetTimeout = global.setTimeout;
    const realClearTimeout = global.clearTimeout;
    const trackedTimeoutHandles: ReturnType<typeof setTimeout>[] = [];
    const clearedTimeoutHandles = new Set<ReturnType<typeof setTimeout>>();

    jest.spyOn(global, "setTimeout").mockImplementation(((
      callback: (...args: any[]) => void,
      delay?: number,
      ...args: any[]
    ) => {
      const handle = realSetTimeout(callback, delay, ...args);
      if (delay === 15_000) {
        trackedTimeoutHandles.push(handle);
      }
      return handle;
    }) as typeof setTimeout);
    jest.spyOn(global, "clearTimeout").mockImplementation(((timeout) => {
      if (timeout != null) {
        clearedTimeoutHandles.add(timeout as ReturnType<typeof setTimeout>);
      }
      return realClearTimeout(timeout);
    }) as typeof clearTimeout);

    const transport = BareHttpTransport();
    const iterator = transport({
      body: createUnaryBody(),
      metadata: new Metadata(),
      method: {
        path: "/spark.SparkService/query_pending_transfers",
        requestStream: false,
        responseStream: false,
      } as any,
      signal: new AbortController().signal,
      url: "http://example.com/test",
    })[Symbol.asyncIterator]();

    const headerPromise = iterator.next();
    for (let attempt = 0; attempt < 10 && onResponse == null; attempt++) {
      await new Promise<void>((resolve) => setImmediate(resolve));
    }
    expect(onResponse).toBeDefined();
    onResponse?.(res as unknown as IncomingMessage);
    await headerPromise;

    const errorPromise = iterator.next().then(
      () => null,
      (error) => error,
    );
    await Promise.resolve();
    res.emit("data", "server exploded");
    res.emit("end");

    const error = await errorPromise;
    expect(error).toBeInstanceOf(ClientError);
    expect(trackedTimeoutHandles).not.toHaveLength(0);
    expect(
      trackedTimeoutHandles.every((handle) =>
        clearedTimeoutHandles.has(handle),
      ),
    ).toBe(true);
  });
});
