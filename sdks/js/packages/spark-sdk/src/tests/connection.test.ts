import { jest } from "@jest/globals";
import fs from "fs";
import { ChannelCredentials, type Channel } from "nice-grpc";
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

  public channelKeyFor(address: string): string {
    return this.makeChannelKey(address, false);
  }

  protected async createChannelWithTLS(
    _address: string,
    _isStreamClientType: boolean = false,
  ): Promise<Channel> {
    await Promise.resolve();
    const ch = new FakeChannel();
    this.createdChannels.push(ch);
    this.createdIsStream.push(_isStreamClientType);
    return ch as unknown as Channel;
  }

  protected async createGrpcClient<T>(
    _definition: AnyServiceDef,
    channel: Channel,
    _withRetries: boolean,
    _middleware?: ClientMiddleware<RetryOptions, object>,
    channelKey?: string,
  ): Promise<T & { close?: () => void }> {
    await Promise.resolve();
    const close =
      channelKey != null
        ? () => TestConnectionManager.releaseChannel(channelKey)
        : channel.close.bind(channel);
    return { close } as T & { close?: () => void };
  }

  protected async authenticate(_address: string): Promise<string> {
    await Promise.resolve();
    return "test-session-token";
  }
}

class CredentialTestConnectionManager extends ConnectionManagerNodeJS {
  public createCredentialsFor(address: string): ChannelCredentials {
    return this.createChannelCredentials(address);
  }
}

function withEnv<T>(
  overrides: Record<string, string | undefined>,
  run: () => T,
): T {
  const previousValues = Object.fromEntries(
    Object.keys(overrides).map((key) => [key, process.env[key]]),
  );

  for (const [key, value] of Object.entries(overrides)) {
    if (value === undefined) {
      delete process.env[key];
    } else {
      process.env[key] = value;
    }
  }

  try {
    return run();
  } finally {
    for (const [key, value] of Object.entries(previousValues)) {
      if (value === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = value;
      }
    }
  }
}

const TLS_ENV_KEYS = [
  "SPARK_DANGEROUSLY_DISABLE_TLS_VERIFICATION",
  "SPARK_LOCAL_INGRESS_HOST",
] as const;

describe("ConnectionManagerNodeJS TLS credentials", () => {
  let createSslSpy: jest.SpiedFunction<typeof ChannelCredentials.createSsl>;
  let previousEnv: Record<string, string | undefined>;

  beforeEach(() => {
    previousEnv = Object.fromEntries(
      TLS_ENV_KEYS.map((key) => [key, process.env[key]]),
    );
    for (const key of TLS_ENV_KEYS) {
      delete process.env[key];
    }

    createSslSpy = jest
      .spyOn(ChannelCredentials, "createSsl")
      .mockReturnValue({} as ChannelCredentials);
  });

  afterEach(() => {
    for (const [key, value] of Object.entries(previousEnv)) {
      if (value === undefined) {
        delete process.env[key];
      } else {
        process.env[key] = value;
      }
    }
    jest.restoreAllMocks();
  });

  test("uses default trusted roots when no custom TLS config is provided", () => {
    const config = new WalletConfigService(
      { network: "REGTEST" },
      new DefaultSparkSigner(),
    );
    const mgr = new CredentialTestConnectionManager(config);

    mgr.createCredentialsFor("https://0.spark.lightspark.com");

    expect(createSslSpy).toHaveBeenCalledWith();
  });

  test("uses exported minikube root CA path for local ingress operators", () => {
    const rootCa = Buffer.from("minikube-root-ca");
    jest.spyOn(fs, "readFileSync").mockReturnValue(rootCa);

    withEnv({ SPARK_LOCAL_INGRESS_HOST: "192.168.49.2" }, () => {
      const config = new WalletConfigService(
        { network: "LOCAL" },
        new DefaultSparkSigner(),
      );
      const mgr = new CredentialTestConnectionManager(config);

      mgr.createCredentialsFor("https://0.spark.minikube.local");

      expect(fs.readFileSync).toHaveBeenCalledWith("/tmp/minikube-ca.pem");
      expect(createSslSpy).toHaveBeenCalledWith(rootCa);
    });
  });

  test("falls back to default trusted roots when local ingress root CA file is missing", () => {
    const missingFileError = new Error(
      "missing root CA",
    ) as NodeJS.ErrnoException;
    missingFileError.code = "ENOENT";
    jest.spyOn(fs, "readFileSync").mockImplementation(() => {
      throw missingFileError;
    });

    withEnv({ SPARK_LOCAL_INGRESS_HOST: "192.168.49.2" }, () => {
      const config = new WalletConfigService(
        { network: "LOCAL" },
        new DefaultSparkSigner(),
      );
      const mgr = new CredentialTestConnectionManager(config);

      mgr.createCredentialsFor("https://0.spark.minikube.local");

      expect(fs.readFileSync).toHaveBeenCalledWith("/tmp/minikube-ca.pem");
      expect(createSslSpy).toHaveBeenCalledWith();
    });
  });

  test("does not apply local ingress root CA path to hosted networks", () => {
    withEnv({ SPARK_LOCAL_INGRESS_HOST: "192.168.49.2" }, () => {
      const config = new WalletConfigService(
        { network: "REGTEST" },
        new DefaultSparkSigner(),
      );
      const mgr = new CredentialTestConnectionManager(config);

      mgr.createCredentialsFor("https://0.spark.lightspark.com");

      expect(createSslSpy).toHaveBeenCalledWith();
    });
  });

  test("does not apply local ingress root CA path to custom non-minikube local operators", () => {
    const readFileSyncSpy = jest.spyOn(fs, "readFileSync");

    withEnv({ SPARK_LOCAL_INGRESS_HOST: "192.168.49.2" }, () => {
      const config = new WalletConfigService(
        { network: "LOCAL" },
        new DefaultSparkSigner(),
      );
      const mgr = new CredentialTestConnectionManager(config);

      mgr.createCredentialsFor("https://operator.example.com");

      expect(readFileSyncSpy).not.toHaveBeenCalled();
      expect(createSslSpy).toHaveBeenCalledWith();
    });
  });

  test("allows explicitly disabled certificate verification for local operators", () => {
    const config = new WalletConfigService(
      {
        network: "LOCAL",
        tls: { dangerouslyDisableCertificateVerification: true },
      },
      new DefaultSparkSigner(),
    );
    const mgr = new CredentialTestConnectionManager(config);

    mgr.createCredentialsFor("https://localhost:8535");

    expect(createSslSpy).toHaveBeenCalledWith(null, null, null, {
      rejectUnauthorized: false,
    });
  });

  test("prefers explicit local verification bypass before reading root CA path", () => {
    const readFileSyncSpy = jest
      .spyOn(fs, "readFileSync")
      .mockImplementation(() => {
        throw new Error("root CA file should not be read");
      });

    withEnv({ SPARK_LOCAL_INGRESS_HOST: "192.168.49.2" }, () => {
      const config = new WalletConfigService(
        {
          network: "LOCAL",
          tls: { dangerouslyDisableCertificateVerification: true },
        },
        new DefaultSparkSigner(),
      );
      const mgr = new CredentialTestConnectionManager(config);

      mgr.createCredentialsFor("https://0.spark.minikube.local");

      expect(readFileSyncSpy).not.toHaveBeenCalled();
      expect(createSslSpy).toHaveBeenCalledWith(null, null, null, {
        rejectUnauthorized: false,
      });
    });
  });

  test("reads local verification bypass from environment when config service is constructed", () => {
    withEnv({ SPARK_DANGEROUSLY_DISABLE_TLS_VERIFICATION: "true" }, () => {
      const config = new WalletConfigService(
        { network: "LOCAL" },
        new DefaultSparkSigner(),
      );
      const mgr = new CredentialTestConnectionManager(config);

      mgr.createCredentialsFor("https://localhost:8535");

      expect(createSslSpy).toHaveBeenCalledWith(null, null, null, {
        rejectUnauthorized: false,
      });
    });
  });

  test("rejects disabled certificate verification for non-local operators", () => {
    const config = new WalletConfigService(
      {
        network: "REGTEST",
        tls: { dangerouslyDisableCertificateVerification: true },
      },
      new DefaultSparkSigner(),
    );
    const mgr = new CredentialTestConnectionManager(config);

    expect(() =>
      mgr.createCredentialsFor("https://0.spark.lightspark.com"),
    ).toThrow("Refusing to disable TLS certificate verification");
    expect(createSslSpy).not.toHaveBeenCalled();
  });

  test("rejects disabled certificate verification for custom non-local operators on local network", () => {
    const config = new WalletConfigService(
      {
        network: "LOCAL",
        signingOperators: {
          "0000000000000000000000000000000000000000000000000000000000000001": {
            id: 0,
            identifier:
              "0000000000000000000000000000000000000000000000000000000000000001",
            address: "https://operator.example.com",
            identityPublicKey:
              "03dfbdff4b6332c220f8fa2ba8ed496c698ceada563fa01b67d9983bfc5c95e763",
          },
        },
        tls: { dangerouslyDisableCertificateVerification: true },
      },
      new DefaultSparkSigner(),
    );
    const mgr = new CredentialTestConnectionManager(config);

    expect(() =>
      mgr.createCredentialsFor("https://operator.example.com"),
    ).toThrow("Refusing to disable TLS certificate verification");
    expect(createSslSpy).not.toHaveBeenCalled();
  });
});

describe("ConnectionManager channel cache", () => {
  test("reuses channel for unary clients and releases once after close", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    await mgr.createSparkClient(address);
    await mgr.createSparkClient(address);

    expect(mgr.createdChannels).toHaveLength(1);
    const ch1 = mgr.createdChannels[0]!;
    expect(ch1.close).not.toHaveBeenCalled();

    await mgr.closeConnections();
    expect(ch1.close).toHaveBeenCalledTimes(1);

    await mgr.createSparkClient(address);
    expect(mgr.createdChannels).toHaveLength(2);
    const ch2 = mgr.createdChannels[1]!;
    await mgr.closeConnections();
    expect(ch2.close).toHaveBeenCalledTimes(1);
    expect(ch1.close).toHaveBeenCalledTimes(1);
  });

  test("creates distinct channels for stream vs unary and closes both on closeConnections", async () => {
    const config = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr = new TestConnectionManager(config);
    const address = "https://0.spark.minikube.local";

    await mgr.createSparkClient(address);
    await mgr.createSparkStreamClient(address);

    // Two channels created: one unary, one stream
    expect(mgr.createdChannels).toHaveLength(2);
    const [unaryCh, streamCh] = mgr.createdChannels;
    expect(mgr.createdIsStream).toEqual([false, true]);

    // closeConnections closes all client types
    await mgr.closeConnections();
    expect(unaryCh!.close).toHaveBeenCalledTimes(1);
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
    const cachedCh = mgr.getChannelForClient("spark", address);
    expect(cachedCh).toBe(ch as unknown as Channel);

    await mgr.closeConnections();
    expect(ch.close).toHaveBeenCalledTimes(1);
  });

  test("spark and tokens share unary channel and closeConnections releases both", async () => {
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

    // closeConnections releases all client types, closing the shared channel
    await mgr.closeConnections();
    expect(ch.close).toHaveBeenCalledTimes(1);

    // Avoid unused variable lint
    expect(sparkClient).toBeDefined();
    expect(tokensClient).toBeDefined();
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

  test("deduplicates concurrent stream channel creation; closeConnections releases all", async () => {
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

    // closeConnections closes all client types including stream
    await mgr.closeConnections();
    expect(ch.close).toHaveBeenCalledTimes(1);

    // Avoid unused variable lint
    expect(s1).toBeDefined();
    expect(s2).toBeDefined();
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
    const cachedCh2 = mgr2.getChannelForClient("spark", address);
    expect(cachedCh2).toBe(ch as unknown as Channel);

    await mgr1.closeConnections();
    // Not closed yet; mgr2 still holds a ref
    expect(ch.close).not.toHaveBeenCalled();

    await mgr2.closeConnections();
    expect(ch.close).toHaveBeenCalledTimes(1);
  });

  test("does not reuse channels across manager instances with different TLS verification modes", async () => {
    const address = "https://localhost:8535";
    const config1 = new WalletConfigService(
      { network: "LOCAL" },
      new DefaultSparkSigner(),
    );
    const mgr1 = new TestConnectionManager(config1);
    const config2 = new WalletConfigService(
      {
        network: "LOCAL",
        tls: { dangerouslyDisableCertificateVerification: true },
      },
      new DefaultSparkSigner(),
    );
    const mgr2 = new TestConnectionManager(config2);

    await mgr1.createSparkClient(address);
    await mgr2.createSparkClient(address);

    expect(mgr1.createdChannels).toHaveLength(1);
    expect(mgr2.createdChannels).toHaveLength(1);
    expect(mgr2.createdChannels[0]).not.toBe(mgr1.createdChannels[0]);

    await mgr1.closeConnections();
    await mgr2.closeConnections();

    expect(mgr1.createdChannels[0]!.close).toHaveBeenCalledTimes(1);
    expect(mgr2.createdChannels[0]!.close).toHaveBeenCalledTimes(1);
  });

  test("changes channel cache key when local ingress root CA contents change", () => {
    const readFileSyncSpy = jest.spyOn(fs, "readFileSync");
    const address = "https://0.spark.minikube.local";

    withEnv({ SPARK_LOCAL_INGRESS_HOST: "192.168.49.2" }, () => {
      const config = new WalletConfigService(
        { network: "LOCAL" },
        new DefaultSparkSigner(),
      );
      const mgr = new TestConnectionManager(config);
      const missingFileError = new Error(
        "missing root CA",
      ) as NodeJS.ErrnoException;
      missingFileError.code = "ENOENT";

      readFileSyncSpy.mockImplementation(() => {
        throw missingFileError;
      });
      const missingCaKey = mgr.channelKeyFor(address);

      readFileSyncSpy.mockReturnValue(Buffer.from("minikube-root-ca-1"));
      const firstCaKey = mgr.channelKeyFor(address);

      readFileSyncSpy.mockReturnValue(Buffer.from("minikube-root-ca-2"));
      const secondCaKey = mgr.channelKeyFor(address);

      expect(firstCaKey).not.toBe(missingCaKey);
      expect(secondCaKey).not.toBe(firstCaKey);
    });
  });
});

describe("ConnectionManager middleware", () => {
  class MiddlewareTestConnectionManager extends ConnectionManagerNodeJS {
    public authCalls = 0;

    protected async createChannelWithTLS(
      _address: string,
      _isStreamClientType: boolean = false,
    ): Promise<Channel> {
      await Promise.resolve();
      return new FakeChannel() as unknown as Channel;
    }

    protected async createGrpcClient<T>(
      _definition: AnyServiceDef,
      channel: Channel,
      _withRetries: boolean,
      _middleware?: ClientMiddleware<RetryOptions, object>,
      channelKey?: string,
    ): Promise<T & { close?: () => void }> {
      await Promise.resolve();
      const close =
        channelKey != null
          ? () => MiddlewareTestConnectionManager.releaseChannel(channelKey)
          : channel.close.bind(channel);
      return { close } as T & { close?: () => void };
    }

    protected async authenticate(_address: string): Promise<string> {
      await Promise.resolve();
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
      request: { id: 1 },
      responseStream: false,
      next: async function* (_request: Req, options: CallOptions) {
        await Promise.resolve();
        yield* [] as never[];
        const auth = options.metadata?.get("Authorization");
        if (invocation === 0) {
          expect(auth).toBe("Bearer t1");
          invocation++;
          throw new Error("token has expired");
        }
        expect(auth).toBe("Bearer t2");
        return "ok";
      },
    };

    const gen = middleware(call, {});
    const result = await gen.next();
    expect(result.done).toBe(true);
    expect(result.value).toBe("ok");
    expect(mgr.authCalls).toBe(2);
  });

  class AuthCachingTestConnectionManager extends ConnectionManagerNodeJS {
    public getChallengeCalls = 0;
    public verifyChallengeCalls = 0;
    // Fixed reference so token expiry is deterministic regardless of clock/time-sync.
    private readonly fixedNow = new Date();
    public getCurrentServerTime(): Date {
      return this.fixedNow;
    }

    protected async createChannelWithTLS(
      _address: string,
      _isStreamClientType: boolean = false,
    ): Promise<Channel> {
      await Promise.resolve();
      return new FakeChannel() as unknown as Channel;
    }

    protected async createGrpcClient<T>(
      _definition: AnyServiceDef,
      channel: Channel,
      _withRetries: boolean,
      _middleware?: ClientMiddleware<RetryOptions, object>,
      channelKey?: string,
    ): Promise<T & { close?: () => void }> {
      await Promise.resolve();
      const close =
        channelKey != null
          ? () => AuthCachingTestConnectionManager.releaseChannel(channelKey)
          : channel.close.bind(channel);

      const fakeAuthClient = {
        get_challenge: async ({ publicKey }: { publicKey: Uint8Array }) => {
          this.getChallengeCalls += 1;
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
        verify_challenge: async () => {
          await Promise.resolve();
          this.verifyChallengeCalls += 1;
          // Derive expiration from the stubbed server time so the TTL check is deterministic
          return {
            sessionToken: "cached-token",
            expirationTimestamp:
              Math.floor(this.fixedNow.getTime() / 1000) + 3600,
          };
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
        request: { id: 1 },
        responseStream: false,
        next: async function* () {
          await Promise.resolve();
          yield* [] as never[];
          return "ok";
        },
      }) as ClientMiddlewareCall<Req, Res>;

    const g1 = middleware(buildCall(), {});
    const g2 = middleware(buildCall(), {});

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
      request: { id: 1 },
      responseStream: false,
      next: async function* () {
        await Promise.resolve();
        yield* [] as never[];
        return "ok";
      },
    };

    const r1 = await middleware(call, {}).next();
    expect(r1.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);

    const r2 = await middleware(call, {}).next();
    expect(r2.value).toBe("ok");
    // Still one call due to token cache
    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);
  });

  /**
   * Regression test for the auth retry refcount bug.
   *
   * Before the fix, authenticate() created the authn gRPC client once
   * (acquireChannel +1 refCount) but called close() on every retry
   * iteration (releaseChannel -1 each time). After enough connection
   * errors the shared unary channel's refCount hit 0 and the channel
   * was permanently closed.
   *
   * The fix moves close() into a single finally block so it's called
   * exactly once regardless of how many retries occur.
   */
  test("auth retries with connection errors do not drain the shared channel refcount", async () => {
    let getChallengeAttempts = 0;

    class AuthRetryTestConnectionManager extends ConnectionManagerNodeJS {
      public createdChannels: FakeChannel[] = [];
      private readonly fixedNow = new Date();
      public getCurrentServerTime(): Date {
        return this.fixedNow;
      }

      protected async createChannelWithTLS(
        _address: string,
        _isStreamClientType: boolean = false,
      ): Promise<Channel> {
        await Promise.resolve();
        const ch = new FakeChannel();
        this.createdChannels.push(ch);
        return ch as unknown as Channel;
      }

      protected async createGrpcClient<T>(
        _definition: AnyServiceDef,
        channel: Channel,
        _withRetries: boolean,
        _middleware?: ClientMiddleware<RetryOptions, object>,
        channelKey?: string,
      ): Promise<T & { close?: () => void }> {
        await Promise.resolve();
        const close =
          channelKey != null
            ? () => AuthRetryTestConnectionManager.releaseChannel(channelKey)
            : channel.close.bind(channel);

        const fakeClient = {
          get_challenge: async ({ publicKey }: { publicKey: Uint8Array }) => {
            await Promise.resolve();
            getChallengeAttempts += 1;
            // Fail with a connection error on the first 3 attempts,
            // then succeed on attempt 4.
            if (getChallengeAttempts <= 3) {
              throw new Error("UNAVAILABLE: read ETIMEDOUT");
            }
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
          verify_challenge: async () => {
            await Promise.resolve();
            return {
              sessionToken: "recovered-token",
              expirationTimestamp:
                Math.floor(this.fixedNow.getTime() / 1000) + 3600,
            };
          },
          close,
        } as unknown as T & { close?: () => void };

        return fakeClient;
      }
    }

    const signer = new DefaultSparkSigner();
    await signer.createSparkWalletFromSeed(new Uint8Array(32));
    const config = new WalletConfigService({ network: "LOCAL" }, signer);
    const mgr = new AuthRetryTestConnectionManager(config);
    const address = "https://refcount-bug.spark.local";

    // Create a spark client — this triggers initial auth (which creates
    // and closes a temporary authn channel) then creates the spark client's
    // channel. After this, the unary channel has refCount = 1.
    await mgr.createSparkClient(address);
    const channelsAfterInit = mgr.createdChannels.length;
    const sparkChannel = mgr.createdChannels[channelsAfterInit - 1]!;

    // Invalidate the cached auth token so the next authenticate() call
    // goes through the full get_challenge / verify_challenge flow.
    (
      AuthRetryTestConnectionManager as unknown as {
        authTokenCache: { clear: () => void };
      }
    ).authTokenCache.clear();

    // Trigger re-authentication. This acquires the EXISTING unary channel
    // (refCount goes to 2), then get_challenge fails 3 times with
    // connection errors before succeeding on attempt 4.
    //
    // With the fix: close() is called once in the finally block (refCount → 1).
    // Without the fix: close() was called on each retry (refCount → -2),
    //   destroying the channel after the second close.
    // The first authenticate() (inside createSparkClient) already consumed
    // attempts 1-4 of the mock. Reset so the re-auth exercises the retry path fresh.
    getChallengeAttempts = 0;
    const token = await (
      mgr as unknown as { authenticate: (address: string) => Promise<string> }
    ).authenticate(address);

    expect(token).toBeDefined();
    expect(getChallengeAttempts).toBe(4); // 3 failures + 1 success

    // The shared spark channel must still be alive — close should NOT
    // have been called (refCount should be 1, not 0).
    expect(sparkChannel.close).not.toHaveBeenCalled();

    // No new channels should have been created during re-auth
    // (the authn client reused the existing unary channel).
    expect(mgr.createdChannels).toHaveLength(channelsAfterInit);

    await mgr.closeConnections();
    // NOW close should be called exactly once (refCount drops to 0)
    expect(sparkChannel.close).toHaveBeenCalledTimes(1);
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
      request: { id: 1 },
      responseStream: false,
      next: async function* () {
        await Promise.resolve();
        yield* [] as never[];
        return "ok";
      },
    };

    const r1 = await middleware1(call, {}).next();
    expect(r1.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(1);
    expect(mgr.verifyChallengeCalls).toBe(1);

    const r2 = await middleware2(call, {}).next();
    expect(r2.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(2);
    expect(mgr.verifyChallengeCalls).toBe(2);

    const r3 = await middleware1(call, {}).next();
    expect(r3.value).toBe("ok");
    expect(mgr.getChallengeCalls).toBe(2);
    expect(mgr.verifyChallengeCalls).toBe(2);
  });
});
