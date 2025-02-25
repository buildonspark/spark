import { sha256 } from "@scure/btc-signer/utils";
import * as fs from "fs";
import { ChannelCredentials, createChannel, createClient, createClientFactory, Metadata, } from "nice-grpc";
import { MockServiceDefinition } from "../proto/mock.js";
import { SparkServiceDefinition } from "../proto/spark.js";
import { Challenge, SparkAuthnServiceDefinition, } from "../proto/spark_authn.js";
// TODO: Some sort of client cleanup
export class ConnectionManager {
    config;
    clients = {};
    constructor(config) {
        this.config = config;
    }
    static createMockClient(address) {
        const channel = this.createChannelWithTLS(address);
        const client = createClient(MockServiceDefinition, channel);
        return { ...client, close: () => channel.close() };
    }
    // TODO: Web transport handles TLS differently, verify that we don't need to do anything
    static createChannelWithTLS(address, certPath) {
        try {
            if (certPath && typeof window === "undefined") {
                // TODO: Verify that this is the correct way to create a channel with TLS
                const cert = fs.readFileSync(certPath);
                return createChannel(address, ChannelCredentials.createSsl(cert));
            }
            else {
                // Fallback to insecure for development
                return createChannel(address, typeof window === "undefined"
                    ? ChannelCredentials.createSsl(null, null, null, {
                        rejectUnauthorized: false,
                    })
                    : undefined);
            }
        }
        catch (error) {
            console.error("Channel creation error:", error);
            throw new Error("Failed to create channel");
        }
    }
    async createSparkClient(address, certPath) {
        if (this.clients[address]) {
            return this.clients[address].client;
        }
        const authToken = await this.authenticate(address);
        const channel = ConnectionManager.createChannelWithTLS(address, certPath);
        const middleware = this.createMiddleWare(address, authToken);
        const client = this.createGrpcClient(SparkServiceDefinition, channel, middleware);
        this.clients[address] = { client, authToken };
        return client;
    }
    async authenticate(address) {
        try {
            const identityPublicKey = await this.config.signer.getIdentityPublicKey();
            const sparkAuthnClient = this.createSparkAuthnGrpcConnection(address);
            const challengeResp = await sparkAuthnClient.get_challenge({
                publicKey: identityPublicKey,
            });
            if (!challengeResp.protectedChallenge?.challenge) {
                throw new Error("Invalid challenge response");
            }
            const challengeBytes = Challenge.encode(challengeResp.protectedChallenge.challenge).finish();
            const hash = sha256(challengeBytes);
            const derSignatureBytes = await this.config.signer.signMessageWithIdentityKey(hash);
            const verifyResp = await sparkAuthnClient.verify_challenge({
                protectedChallenge: challengeResp.protectedChallenge,
                signature: derSignatureBytes,
                publicKey: identityPublicKey,
            });
            sparkAuthnClient.close?.();
            return verifyResp.sessionToken;
        }
        catch (error) {
            console.error("Authentication error:", error);
            throw new Error(`Authentication failed: ${error.message}`);
        }
    }
    createSparkAuthnGrpcConnection(address, certPath) {
        const channel = ConnectionManager.createChannelWithTLS(address, certPath);
        return this.createGrpcClient(SparkAuthnServiceDefinition, channel);
    }
    createMiddleWare(address, authToken) {
        if (typeof window === "undefined") {
            return this.createNodeMiddleware(address, authToken);
        }
        else {
            return this.createBrowserMiddleware(address, authToken);
        }
    }
    createNodeMiddleware(address, initialAuthToken) {
        return async function* (call, options) {
            try {
                yield* call.next(call.request, {
                    ...options,
                    metadata: Metadata(options.metadata).set("Authorization", `Bearer ${this.clients[address]?.authToken || initialAuthToken}`),
                });
            }
            catch (error) {
                if (error.message?.includes("token has expired")) {
                    const newAuthToken = await this.authenticate(address);
                    // @ts-ignore - We can only get here if the client exists
                    this.clients[address].authToken = newAuthToken;
                    yield* call.next(call.request, {
                        ...options,
                        metadata: Metadata(options.metadata).set("Authorization", `Bearer ${newAuthToken}`),
                    });
                }
                throw error;
            }
        }.bind(this);
    }
    createBrowserMiddleware(address, initialAuthToken) {
        return async function* (call, options) {
            try {
                yield* call.next(call.request, {
                    ...options,
                    metadata: Metadata(options.metadata)
                        .set("Authorization", `Bearer ${this.clients[address]?.authToken || initialAuthToken}`)
                        .set("X-Requested-With", "XMLHttpRequest")
                        .set("X-Grpc-Web", "1")
                        .set("Content-Type", "application/grpc-web+proto"),
                });
            }
            catch (error) {
                if (error.message?.includes("token has expired")) {
                    const newAuthToken = await this.authenticate(address);
                    // @ts-ignore - We can only get here if the client exists
                    this.clients[address].authToken = newAuthToken;
                    yield* call.next(call.request, {
                        ...options,
                        metadata: Metadata(options.metadata)
                            .set("Authorization", `Bearer ${newAuthToken}`)
                            .set("X-Requested-With", "XMLHttpRequest")
                            .set("X-Grpc-Web", "1")
                            .set("Content-Type", "application/grpc-web+proto"),
                    });
                }
                throw error;
            }
        }.bind(this);
    }
    createGrpcClient(defintion, channel, middleware) {
        const clientFactory = createClientFactory();
        if (middleware) {
            clientFactory.use(middleware);
        }
        const client = clientFactory.create(defintion, channel);
        return {
            ...client,
            close: channel.close?.bind(channel),
        };
    }
}
//# sourceMappingURL=connection.js.map