import { BinaryReader, BinaryWriter } from "@bufbuild/protobuf/wire";
import { type CallContext, type CallOptions } from "nice-grpc-common";
export declare const protobufPackage = "spark_authn";
/** Challenge represents the core challenge data */
export interface Challenge {
    /** Protocol version for backward compatibility */
    version: number;
    /** Timestamp when challenge was issued (UTC Unix seconds) */
    timestamp: number;
    /** Random nonce to prevent replay attacks (32 bytes) */
    nonce: Uint8Array;
    /** The public key this challenge is intended for (uncompressed secp256k1 public key) */
    publicKey: Uint8Array;
}
/** ProtectedChallenge wraps a Challenge with a server HMAC */
export interface ProtectedChallenge {
    /** Protocol version for backward compatibility */
    version: number;
    /** The core challenge data */
    challenge: Challenge | undefined;
    /** Server's HMAC of the Challenge */
    serverHmac: Uint8Array;
}
/** Request to initiate an authentication challenge */
export interface GetChallengeRequest {
    /** Client's public key (uncompressed secp256k1 public key) */
    publicKey: Uint8Array;
}
/** Response containing the protected challenge */
export interface GetChallengeResponse {
    /** The protected challenge from the server */
    protectedChallenge: ProtectedChallenge | undefined;
}
/** Request to verify a signed challenge */
export interface VerifyChallengeRequest {
    /** The protected challenge from the server */
    protectedChallenge: ProtectedChallenge | undefined;
    /** Client's secp256k1 signature of the Challenge */
    signature: Uint8Array;
    /** Client's public key (uncompressed secp256k1 public key) */
    publicKey: Uint8Array;
}
/** Response after successful authentication */
export interface VerifyChallengeResponse {
    /** Session token for subsequent API calls */
    sessionToken: string;
    /** Token expiration timestamp (UTC Unix seconds) */
    expirationTimestamp: number;
}
export declare const Challenge: MessageFns<Challenge>;
export declare const ProtectedChallenge: MessageFns<ProtectedChallenge>;
export declare const GetChallengeRequest: MessageFns<GetChallengeRequest>;
export declare const GetChallengeResponse: MessageFns<GetChallengeResponse>;
export declare const VerifyChallengeRequest: MessageFns<VerifyChallengeRequest>;
export declare const VerifyChallengeResponse: MessageFns<VerifyChallengeResponse>;
export type SparkAuthnServiceDefinition = typeof SparkAuthnServiceDefinition;
export declare const SparkAuthnServiceDefinition: {
    readonly name: "SparkAuthnService";
    readonly fullName: "spark_authn.SparkAuthnService";
    readonly methods: {
        /** Request a new authentication challenge for a public key */
        readonly get_challenge: {
            readonly name: "get_challenge";
            readonly requestType: MessageFns<GetChallengeRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<GetChallengeResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
        /** Verify a signed challenge and return a session token */
        readonly verify_challenge: {
            readonly name: "verify_challenge";
            readonly requestType: MessageFns<VerifyChallengeRequest>;
            readonly requestStream: false;
            readonly responseType: MessageFns<VerifyChallengeResponse>;
            readonly responseStream: false;
            readonly options: {};
        };
    };
};
export interface SparkAuthnServiceImplementation<CallContextExt = {}> {
    /** Request a new authentication challenge for a public key */
    get_challenge(request: GetChallengeRequest, context: CallContext & CallContextExt): Promise<DeepPartial<GetChallengeResponse>>;
    /** Verify a signed challenge and return a session token */
    verify_challenge(request: VerifyChallengeRequest, context: CallContext & CallContextExt): Promise<DeepPartial<VerifyChallengeResponse>>;
}
export interface SparkAuthnServiceClient<CallOptionsExt = {}> {
    /** Request a new authentication challenge for a public key */
    get_challenge(request: DeepPartial<GetChallengeRequest>, options?: CallOptions & CallOptionsExt): Promise<GetChallengeResponse>;
    /** Verify a signed challenge and return a session token */
    verify_challenge(request: DeepPartial<VerifyChallengeRequest>, options?: CallOptions & CallOptionsExt): Promise<VerifyChallengeResponse>;
}
type Builtin = Date | Function | Uint8Array | string | number | boolean | undefined;
export type DeepPartial<T> = T extends Builtin ? T : T extends globalThis.Array<infer U> ? globalThis.Array<DeepPartial<U>> : T extends ReadonlyArray<infer U> ? ReadonlyArray<DeepPartial<U>> : T extends {
    $case: string;
} ? {
    [K in keyof Omit<T, "$case">]?: DeepPartial<T[K]>;
} & {
    $case: T["$case"];
} : T extends {} ? {
    [K in keyof T]?: DeepPartial<T[K]>;
} : Partial<T>;
export interface MessageFns<T> {
    encode(message: T, writer?: BinaryWriter): BinaryWriter;
    decode(input: BinaryReader | Uint8Array, length?: number): T;
    fromJSON(object: any): T;
    toJSON(message: T): unknown;
    create(base?: DeepPartial<T>): T;
    fromPartial(object: DeepPartial<T>): T;
}
export {};
