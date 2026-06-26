import { afterEach, beforeEach, describe, expect, it } from "@jest/globals";
import { createXHRFetch } from "../services/xhr-fetch.js";

interface MockHandlers {
  onload: (() => void) | null;
  onerror: (() => void) | null;
  ontimeout: (() => void) | null;
  onabort: (() => void) | null;
}

class MockXHR implements MockHandlers {
  static last: MockXHR | undefined;

  method = "";
  url = "";
  responseType = "";
  timeout = 0;
  // React Native's XMLHttpRequest defaults withCredentials to true; mirror that
  // so the credentials test exercises the platform-default inheritance.
  withCredentials = true;
  status = 0;
  statusText = "";
  response: ArrayBuffer | null = null;
  responseHeadersRaw = "";
  requestHeaders: Record<string, string> = {};
  sentBody: unknown = "__unsent__";

  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  onabort: (() => void) | null = null;

  constructor() {
    MockXHR.last = this;
  }

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(key: string, value: string) {
    this.requestHeaders[key.toLowerCase()] = value;
  }

  getAllResponseHeaders() {
    return this.responseHeadersRaw;
  }

  send(body?: unknown) {
    this.sentBody = body;
  }

  abort() {
    this.onabort?.();
  }

  succeed(
    status: number,
    body: ArrayBuffer | string,
    headers = "",
    statusText?: string,
  ) {
    this.status = status;
    this.statusText = statusText ?? (status === 200 ? "OK" : "");
    this.response =
      typeof body === "string" ? new TextEncoder().encode(body).buffer : body;
    this.responseHeadersRaw = headers;
    this.onload?.();
  }
}

// Let queued microtasks (e.g. awaiting a Request body) and the Promise
// executor run so the MockXHR instance exists before the test drives it.
const flush = () => new Promise((resolve) => setImmediate(resolve));

describe("createXHRFetch", () => {
  let originalXHR: typeof XMLHttpRequest | undefined;

  beforeEach(() => {
    originalXHR = globalThis.XMLHttpRequest;
    globalThis.XMLHttpRequest = MockXHR as unknown as typeof XMLHttpRequest;
    MockXHR.last = undefined;
  });

  afterEach(() => {
    globalThis.XMLHttpRequest = originalXHR as typeof XMLHttpRequest;
  });

  it("resolves a JSON response and decodes it", async () => {
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql");
    await flush();

    const xhr = MockXHR.last!;
    expect(xhr.method).toBe("GET");
    expect(xhr.responseType).toBe("arraybuffer");
    xhr.succeed(
      200,
      JSON.stringify({ ok: true }),
      "content-type: application/json",
    );

    const res = await promise;
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("application/json");
    expect(await res.json()).toEqual({ ok: true });
  });

  it("preserves binary response bodies byte-for-byte", async () => {
    const bytes = new Uint8Array([0, 1, 2, 200, 253, 254, 255]);
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/blob");
    await flush();

    MockXHR.last!.succeed(200, bytes.buffer);

    const res = await promise;
    const out = new Uint8Array(await res.arrayBuffer());
    expect(Array.from(out)).toEqual(Array.from(bytes));
  });

  it("decodes non-ASCII JSON as UTF-8 and keeps the raw bytes readable", async () => {
    const payload = { memo: "café — 50€ 🎉" };
    const bytes = new TextEncoder().encode(JSON.stringify(payload));
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql");
    await flush();

    MockXHR.last!.succeed(200, bytes.buffer);

    const res = await promise;
    // text()/json() must decode UTF-8 correctly (RN's whatwg-fetch would
    // otherwise decode the ArrayBuffer body as Latin-1).
    expect(await res.json()).toEqual(payload);
    // The override leaves the raw bytes intact for arrayBuffer().
    const out = new Uint8Array(await res.arrayBuffer());
    expect(Array.from(out)).toEqual(Array.from(bytes));
  });

  it("maps explicit fetch credentials onto withCredentials and inherits the platform default otherwise", async () => {
    const fetch = createXHRFetch();

    // Unspecified credentials: leave the platform default untouched so
    // installing this as the global fetch doesn't change the host app's cookie
    // behavior (RN's XHR defaults withCredentials to true).
    const defaultPromise = fetch("https://ssp.example/graphql");
    await flush();
    const defaultXhr = MockXHR.last!;
    expect(defaultXhr.withCredentials).toBe(true);
    defaultXhr.succeed(200, "{}");
    await defaultPromise;

    const omitPromise = fetch("https://ssp.example/graphql", {
      credentials: "omit",
    });
    await flush();
    const omitXhr = MockXHR.last!;
    expect(omitXhr.withCredentials).toBe(false);
    omitXhr.succeed(200, "{}");
    await omitPromise;

    const includePromise = fetch("https://ssp.example/graphql", {
      credentials: "include",
    });
    await flush();
    const includeXhr = MockXHR.last!;
    expect(includeXhr.withCredentials).toBe(true);
    includeXhr.succeed(200, "{}");
    await includePromise;
  });

  it("sends method, headers, and string body from init", async () => {
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql", {
      method: "POST",
      headers: { "x-test": "1" },
      body: '{"query":"{ me }"}',
    });
    await flush();

    const xhr = MockXHR.last!;
    expect(xhr.method).toBe("POST");
    expect(xhr.requestHeaders["x-test"]).toBe("1");
    expect(xhr.sentBody).toBe('{"query":"{ me }"}');
    xhr.succeed(200, "{}");
    await promise;
  });

  it("honors method, headers, and body from a Request input", async () => {
    const fetch = createXHRFetch();
    const request = new Request("https://ssp.example/graphql", {
      method: "POST",
      headers: { "x-from-request": "yes" },
      body: "payload",
    });
    const promise = fetch(request);
    await flush();

    const xhr = MockXHR.last!;
    expect(xhr.url).toBe("https://ssp.example/graphql");
    expect(xhr.method).toBe("POST");
    expect(xhr.requestHeaders["x-from-request"]).toBe("yes");
    expect(xhr.sentBody).toBe("payload");
    xhr.succeed(200, "{}");
    await promise;
  });

  it("sends a Uint8Array body byte-for-byte (the production SSP path)", async () => {
    const fetch = createXHRFetch();
    const body = new TextEncoder().encode(JSON.stringify({ query: "{ me }" }));
    const promise = fetch("https://ssp.example/graphql", {
      method: "POST",
      body,
    });
    await flush();

    const xhr = MockXHR.last!;
    // Forwarded as-is — not coerced to a string or copied — so XHR transmits
    // the raw bytes without touching BlobModule.
    expect(xhr.sentBody).toBeInstanceOf(Uint8Array);
    expect(Array.from(xhr.sentBody as Uint8Array)).toEqual(Array.from(body));
    xhr.succeed(200, "{}");
    await promise;
  });

  it("exposes non-2xx responses without throwing", async () => {
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql");
    await flush();

    MockXHR.last!.succeed(
      401,
      JSON.stringify({ errors: [{ message: "nope" }] }),
      "",
      "Unauthorized",
    );

    const res = await promise;
    expect(res.ok).toBe(false);
    expect(res.status).toBe(401);
    expect(res.statusText).toBe("Unauthorized");
    expect(await res.json()).toEqual({ errors: [{ message: "nope" }] });
  });

  it("returns a null-body response for an empty body (e.g. 204)", async () => {
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql");
    await flush();

    // An empty ArrayBuffer must not be passed to the Response constructor, which
    // throws "Response with null body status cannot have body" for 204.
    MockXHR.last!.succeed(204, new ArrayBuffer(0));

    const res = await promise;
    expect(res.status).toBe(204);
    expect(await res.text()).toBe("");
  });

  it("rejects on transport error", async () => {
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql");
    await flush();
    MockXHR.last!.onerror?.();
    await expect(promise).rejects.toThrow(/failed/i);
  });

  it("rejects with a TimeoutError on timeout", async () => {
    const fetch = createXHRFetch({ timeoutMs: 5 });
    const promise = fetch("https://ssp.example/graphql");
    await flush();

    const xhr = MockXHR.last!;
    expect(xhr.timeout).toBe(5);
    xhr.ontimeout?.();
    await expect(promise).rejects.toMatchObject({ name: "TimeoutError" });
  });

  it("rejects when the abort signal fires", async () => {
    const controller = new AbortController();
    const fetch = createXHRFetch();
    const promise = fetch("https://ssp.example/graphql", {
      signal: controller.signal,
    });
    await flush();

    controller.abort();
    await expect(promise).rejects.toMatchObject({ name: "AbortError" });
  });
});
