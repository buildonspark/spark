import { sha256 } from "@scure/btc-signer/utils";
import * as fs from "fs";
import { ChannelCredentials, createChannel, createClient, createClientFactory, Metadata, } from "nice-grpc";
import { MockServiceDefinition } from "../proto/mock.js";
import { SparkServiceDefinition } from "../proto/spark.js";
import { Challenge, SparkAuthnServiceDefinition, } from "../proto/spark_authn.js";
export class ConnectionManager {
    config;
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
        const authToken = await this.authenticate(address);
        const channel = ConnectionManager.createChannelWithTLS(address, certPath);
        return this.createGrpcClient(SparkServiceDefinition, channel, this.createMiddleWare(authToken));
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
    createMiddleWare(authToken) {
        if (typeof window === "undefined") {
            return this.createNodeMiddleWare(authToken);
        }
        else {
            return this.createBrowserMiddleWare(authToken);
        }
    }
    createNodeMiddleWare(authToken) {
        return (call, options) => {
            return call.next(call.request, {
                ...options,
                metadata: Metadata(options.metadata).set("Authorization", `Bearer ${authToken}`),
            });
        };
    }
    createBrowserMiddleWare(authToken) {
        return (call, options) => {
            return call.next(call.request, {
                ...options,
                metadata: Metadata(options.metadata)
                    .set("Authorization", `Bearer ${authToken}`)
                    .set("X-Requested-With", "XMLHttpRequest")
                    .set("X-Grpc-Web", "1") // Explicitly set gRPC-web header
                    .set("Content-Type", "application/grpc-web+proto"), // Explicitly set content type
            });
        };
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