import { jest } from "@jest/globals";
import type { Channel } from "nice-grpc";
import type {
  ClientMiddleware,
  ClientMiddlewareCall,
  MethodDescriptor,
  CallOptions,
} from "nice-grpc-common";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { WalletConfigService } from "../services/config.js";
import type { RetryOptions } from "nice-grpc-client-middleware-retry";
import type { SparkAuthnServiceDefinition } from "../proto/spark_authn.js";
import type { SparkServiceDefinition } from "../proto/spark.js";
import type { SparkTokenServiceDefinition } from "../proto/spark_token.js";
import { DefaultSparkSigner } from "../signer/signer.js";
import type { SparkCallOptions } from "../types/grpc.js";

class FakeChannel {
  public close = jest.fn<() => void>();
}

type AnyServiceDef =
  | SparkAuthnServiceDefinition
  | SparkServiceDefinition
  | SparkTokenServiceDefinition;

class TestConnectionManager extends ConnectionManagerNodeJS {
  public createdChannels: FakeChannel[] = [];
  public createdIsStream: boolean[] = [];

  protected async createChannelWithTLS(
    _address: string,
    _isStreamClientType: boolean = false,
  ): Promise<Channel> {
    const ch = new FakeChannel();
    this.createdChannels.push(ch);
    this.createdIsStream.push(_isStreamClientType);
    return ch as unknown as Channel;
  }

  protected async createGrpcClient<T>(
    _definition: AnyServiceDef,
    channel: Channel,
    _withRetries: boolean,
    _middleware?: ClientMiddleware<RetryOptions, {}>,
    channelKey?: string,
  ): Promise<T & { close?: () => void }> {
    const close =
      channelKey != null
        ? () => TestConnectionManager.releaseChannel(channelKey)
        : (channel.close.bind(channel) as () => void);
    return { close } as T & { close?: () => void };
  }

  protected async authenticate(_address: string): Promise<string> {
    return "test-session-token";
  }
}

describe("ConnectionManager channel cache", () => {
  test("reuses channel for unary clients and releases once after close", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const client1 = await mgr.createSparkClient(address);
    const client2 = await mgr.createSparkClient(address);

    expect(mgr.createdChannels).toHaveLength(1);
    const ch1 = mgr.createdChannels[0]!;
    expect(ch1.close).not.toHaveBeenCalled();

    await mgr.closeConnections();
    expect(ch1.close).toHaveBeenCalledTimes(1);

    const client3 = await mgr.createSparkClient(address);
    expect(mgr.createdChannels).toHaveLength(2);
    const ch2 = mgr.createdChannels[1]!;
    await mgr.closeConnections();
    expect(ch2.close).toHaveBeenCalledTimes(1);
    expect(ch1.close).toHaveBeenCalledTimes(1);
  });

  test("creates distinct channels for stream vs unary and does not close stream on closeConnections", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const unaryClient = await mgr.createSparkClient(address);
    const streamClient = await mgr.createSparkStreamClient(address);

    // Two channels created: one unary, one stream
    expect(mgr.createdChannels).toHaveLength(2);
    const [unaryCh, streamCh] = mgr.createdChannels;
    expect(mgr.createdIsStream).toEqual([false, true]);

    // closeConnections closes unary clients, not stream
    await mgr.closeConnections();
    expect(unaryCh!.close).toHaveBeenCalledTimes(1);
    expect(streamCh!.close).not.toHaveBeenCalled();

    // Now manually close stream by invoking its optional close (release via channelKey)
    streamClient.close?.();
    expect(streamCh!.close).toHaveBeenCalledTimes(1);
  });

  test("deduplicates concurrent channel creation and exposes channel via getChannelForClient", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const [c1, c2, c3] = await Promise.all([
      mgr.createSparkClient(address),
      mgr.createSparkClient(address),
      mgr.createSparkClient(address),
    ]);

    expect(c1).toBeDefined();
    expect(c2).toBeDefined();
    expect(c3).toBeDefined();

    // Only one underlying channel should be created due to inflight dedup
    expect(mgr.createdChannels).toHaveLength(1);
    const ch = mgr.createdChannels[0]!;

    // The channel returned by getChannelForClient should be the same object
    const cachedCh = await mgr.getChannelForClient("spark", address);
    expect(cachedCh).toBe(ch as unknown as Channel);

    await mgr.closeConnections();
    expect(ch.close).toHaveBeenCalledTimes(1);
  });

  test("spark and tokens share unary channel and require both releases", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const sparkClient = await mgr.createSparkClient(address);
    const tokensClient = await mgr.createSparkTokenClient(address);

    // Only one underlying unary channel should be created and shared
    expect(mgr.createdChannels).toHaveLength(1);
    const ch = mgr.createdChannels[0]!;

    // Closing only spark clients should not close the channel yet (tokens still holds a ref)
    await mgr.closeConnections();
    expect(ch.close).not.toHaveBeenCalled();

    // Now release the tokens client to close the underlying channel
    tokensClient.close?.();
    expect(ch.close).toHaveBeenCalledTimes(1);

    // Avoid unused variable lint for sparkClient
    expect(sparkClient).toBeDefined();
  });

  test("client.close is idempotent and channel closes once", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const client = await mgr.createSparkClient(address);
    expect(mgr.createdChannels).toHaveLength(1);
    const ch = mgr.createdChannels[0]!;

    client.close?.();
    client.close?.();

    expect(ch.close).toHaveBeenCalledTimes(1);
  });

  test("creates separate channels for different addresses", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address1 = "https://0.spark.minikube.local";
    const address2 = "https://1.spark.minikube.local";

    await mgr.createSparkClient(address1);
    await mgr.createSparkClient(address2);

    expect(mgr.createdChannels).toHaveLength(2);
    const [ch1, ch2] = mgr.createdChannels;
    expect(ch1).not.toBe(ch2);

    await mgr.closeConnections();
    expect(ch1!.close).toHaveBeenCalledTimes(1);
    expect(ch2!.close).toHaveBeenCalledTimes(1);
  });

  test("deduplicates concurrent stream channel creation; manual close required", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const [s1, s2] = await Promise.all([
      mgr.createSparkStreamClient(address),
      mgr.createSparkStreamClient(address),
    ]);

    // Only one stream channel created
    expect(mgr.createdChannels).toHaveLength(1);
    expect(mgr.createdIsStream).toEqual([true]);
    const ch = mgr.createdChannels[0]!;

    // closeConnections should not close stream channels - closed via AbortController in SparkWallet
    await mgr.closeConnections();
    expect(ch.close).not.toHaveBeenCalled();

    // Releasing both stream clients should close once
    s1.close?.();
    s2.close?.();
    expect(ch.close).toHaveBeenCalledTimes(1);
  });

  test("reuses channel across manager instances and releases after both close", async () => {
    const config1 = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr1 = new TestConnectionManager(config1);
    const config2 = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr2 = new TestConnectionManager(config2);
    const address = "https://0.spark.minikube.local";

    await mgr1.createSparkClient(address);
    expect(mgr1.createdChannels).toHaveLength(1);
    const ch = mgr1.createdChannels[0]!;

    await mgr2.createSparkClient(address);
    // Should reuse static cached channel; no new channel created by mgr2
    expect(mgr2.createdChannels).toHaveLength(0);
    const cachedCh2 = await mgr2.getChannelForClient("spark", address);
    expect(cachedCh2).toBe(ch as unknown as Channel);

    await mgr1.closeConnections();
    // Not closed yet; mgr2 still holds a ref
    expect(ch.close).not.toHaveBeenCalled();

    await mgr2.closeConnections();
    expect(ch.close).toHaveBeenCalledTimes(1);
  });
});

describe("ConnectionManager middleware", () => {
  class MiddlewareTestConnectionManager extends ConnectionManagerNodeJS {
    public authCalls = 0;

    protected async createChannelWithTLS(
      _address: string,
      _isStreamClientType: boolean = false,
    ): Promise<Channel> {
      return new FakeChannel() as unknown as Channel;
    }

    protected async createGrpcClient<T>(
      _definition: AnyServiceDef,
      channel: Channel,
      _withRetries: boolean,
      _middleware?: ClientMiddleware<RetryOptions, {}>,
      channelKey?: string,
    ): Promise<T & { close?: () => void }> {
      const close =
        channelKey != null
          ? () => MiddlewareTestConnectionManager.releaseChannel(channelKey)
          : (channel.close.bind(channel) as () => void);
      return { close } as T & { close?: () => void };
    }

    protected async authenticate(_address: string): Promise<string> {
      this.authCalls += 1;
      return this.authCalls === 1 ? "t1" : "t2";
    }

    public getMiddlewareForTest(address: string) {
      return this.createMiddleware(address);
    }
  }

  test("middleware retries on token expiry and refreshes Authorization header", async () => {
    const signer = new DefaultSparkSigner();
    await signer.createSparkWalletFromSeed(new Uint8Array(32));
    const config = new WalletConfigService({ network: "LOCAL" }, signer);
    const mgr = new MiddlewareTestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    const middleware = mgr.getMiddlewareForTest(address);

    type Req = { id: number };
    type Res = string;

    let invocation = 0;
    const method: MethodDescriptor = {
      path: "/spark.SparkService/dummy",
      requestStream: false,
      responseStream: false,
      options: {},
    };

    const call: ClientMiddlewareCall<Req, Res> = {
      method,
      requestStream: false,
      request: { id: 1 } as Req,
      responseStream: false,
      next: async function* (_request: Req, options: CallOptions) {
        const auth = options.metadata?.get("Authorization");
        if (invocation === 0) {
          expect(auth).toBe("Bearer t1");
          invocation++;
          throw new Error("token has expired");
        }
        expect(auth).toBe("Bearer t2");
        return "ok" as Res;
      },
    };

    const gen = middleware(call, {} as unknown as SparkCallOptions);
    const result = await gen.next();
    expect(result.done).toBe(true);
    expect(result.value).toBe("ok");
    expect(mgr.authCalls).toBe(2);
  });

  class AuthCachingTestConnectionManager extends ConnectionManagerNodeJS {
    public getChallengeCalls = 0;
    public verifyChallengeCalls = 0;

    protected async createChannelWithTLS(
      _address: string,
      _isStreamClientType: boolean = false,
    ): Promise<Channel> {
      return new FakeChannel() as unknown as Channel;
    }

    protected async createGrpcClient<T>(
      _definition: AnyServiceDef,
      channel: Channel,
      _withRetries: boolean,
      _middleware?: ClientMiddleware<RetryOptions, {}>,
      channelKey?: string,
    ): Promise<T & { close?: () => void }> {
      const close =
        channelKey != null
          ? () => AuthCachingTestConnectionManager.releaseChannel(channelKey)
          : (channel.close.bind(channel) as () => void);

      const self = this;
      const fakeAuthClient = {
        async get_challenge({ publicKey }: { publicKey: Uint8Array }) {
          self.getChallengeCalls += 1;
          await new Promise((r) => setTimeout(r, 5));
          return {
            protectedChallenge: {
              version: 1,
              challenge: {
                version: 1,
                timestamp: 1,
                nonce: new Uint8Array([1]),
                publicKey,
              },
              serverHmac: new Uint8Array([2]),
            },
          };
        },
        async verify_challenge() {
          self.verifyChallengeCalls += 1;
          return { sessionToken: "cached-token", expirationTimestamp: 0 };
        },
        close,
      } as unknown as T & { close?: () => void };

      return fakeAuthClient;
    }

    public getMiddlewareForTest(address: string) {
      return this.createMiddleware(address);
    }
  }

  test("deduplicates concurrent authenticate across middleware calls", async () => {
    const signer = new DefaultSparkSigner();
    await signer.createSparkWalletFromSeed(new Uint8Array(32));
    const config = new WalletConfigService({ network: "LOCAL" }, signer);
    const mgr = new AuthCachingTestConnectionManager(config);
    const address = "https://authdedup.spark.local";

    const middleware = mgr.getMiddlewareForTest(address);

    type Req = { id: number };
    type Res = string;
    const method: MethodDescriptor = {
      path: "/spark.SparkService/dummy",
      requestStream: false,
      responseStream: false,
      options: {},
    };
    const buildCall = () =>
      ({
        method,
        requestStream: false,
        request: { id: 1 } as Req,
        responseStream: false,
        next: async function* () {
          return "ok" as Res;
        },
      }) as ClientMiddlewareCall<Req, Res>;

    const g1 = middleware(buildCall(), {} as unknown as SparkCallOptions);
    const g2 = middleware(buildCall(), {} as unknown as SparkCallOptions);

    const [r1, r2] = await Promise.all([g1.next(), g2.next()]);
    expect(r1.value).toBe("ok");
    expect(r2.value).toBe("ok");

    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);
  });

  test("reuses cached token across sequential middleware calls", async () => {
    const signer = new DefaultSparkSigner();
    await signer.createSparkWalletFromSeed(new Uint8Array(32));
    const config = new WalletConfigService({ network: "LOCAL" }, signer);
    const mgr = new AuthCachingTestConnectionManager(config);
    const address = "https://authcache.spark.local";

    const middleware = mgr.getMiddlewareForTest(address);

    type Req = { id: number };
    type Res = string;
    const method: MethodDescriptor = {
      path: "/spark.SparkService/dummy",
      requestStream: false,
      responseStream: false,
      options: {},
    };
    const call: ClientMiddlewareCall<Req, Res> = {
      method,
      requestStream: false,
      request: { id: 1 } as Req,
      responseStream: false,
      next: async function* () {
        return "ok" as Res;
      },
    };

    const r1 = await (
      await middleware(call, {} as unknown as SparkCallOptions)
    ).next();
    expect(r1.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);

    const r2 = await (
      await middleware(call, {} as unknown as SparkCallOptions)
    ).next();
    expect(r2.value).toBe("ok");
    // Still one call due to token cache
    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);
  });

  test("per-address auth scoping: same signer, two addresses => two auth flows", async () => {
    const signer = new DefaultSparkSigner();
    await signer.createSparkWalletFromSeed(new Uint8Array(32));
    const config = new WalletConfigService({ network: "LOCAL" }, signer);
    const mgr = new AuthCachingTestConnectionManager(config);

    const address1 = "https://authscope-0.spark.local";
    const address2 = "https://authscope-1.spark.local";

    const middleware1 = mgr.getMiddlewareForTest(address1);
    const middleware2 = mgr.getMiddlewareForTest(address2);

    type Req = { id: number };
    type Res = string;
    const method: MethodDescriptor = {
      path: "/spark.SparkService/dummy",
      requestStream: false,
      responseStream: false,
      options: {},
    };
    const call: ClientMiddlewareCall<Req, Res> = {
      method,
      requestStream: false,
      request: { id: 1 } as Req,
      responseStream: false,
      next: async function* () {
        return "ok" as Res;
      },
    };

    const r1 = await (
      await middleware1(call, {} as unknown as SparkCallOptions)
    ).next();
    expect(r1.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);

    const r2 = await (
      await middleware2(call, {} as unknown as SparkCallOptions)
    ).next();
    expect(r2.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(2);
    expect(mgr.verifyChallengeCalls).toBe(2);

    const r3 = await (
      await middleware1(call, {} as unknown as SparkCallOptions)
    ).next();
    expect(r3.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(2);
    expect(mgr.verifyChallengeCalls).toBe(2);
  });
});
